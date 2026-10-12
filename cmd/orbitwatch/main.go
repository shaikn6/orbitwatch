// Command orbitwatch screens the public satellite catalog for close
// approaches and writes the data the globe viewer shows.
//
//	orbitwatch fetch    refresh the cached element sets from CelesTrak
//	orbitwatch screen   compute upcoming close approaches and write web/data
//	orbitwatch watch    keep doing both on a schedule, announcing changes on Kafka
//	orbitwatch serve    serve the viewer
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shaikn6/orbitwatch/internal/catalog"
	"github.com/shaikn6/orbitwatch/internal/fetch"
	"github.com/shaikn6/orbitwatch/internal/pipeline"
	"github.com/shaikn6/orbitwatch/internal/screen"
	"github.com/shaikn6/orbitwatch/internal/sgp4"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: orbitwatch fetch|screen|watch|serve [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "fetch":
		runFetch(os.Args[2:])
	case "screen":
		runScreen(os.Args[2:])
	case "watch":
		runWatch(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

// kind guesses what an object is. The public element sets do not say, but
// debris and rocket bodies are named as such, and the active list is its
// own file.
func kind(name string, active bool) string {
	switch {
	case strings.Contains(name, " DEB"):
		return "debris"
	case strings.Contains(name, " R/B"):
		return "rocket body"
	case active:
		return "active satellite"
	}
	return "inactive"
}

type viewerCatalog struct {
	Generated time.Time `json:"generated"`
	// Parallel arrays keep the file small: one entry per object.
	ID    []int     `json:"id"`
	Name  []string  `json:"name"`
	Kind  []string  `json:"kind"`
	Epoch []float64 `json:"epoch"` // unix seconds
	N     []float64 `json:"n"`     // mean motion, rad/s
	E     []float64 `json:"e"`
	I     []float64 `json:"i"` // radians, as are the three below
	RAAN  []float64 `json:"raan"`
	ArgP  []float64 `json:"argp"`
	M     []float64 `json:"m"`
}

type viewerEvent struct {
	A           int       `json:"a"`
	BID         int       `json:"b"`
	NameA       string    `json:"name_a"`
	NameB       string    `json:"name_b"`
	KindA       string    `json:"kind_a"`
	KindB       string    `json:"kind_b"`
	TCA         time.Time `json:"tca"`
	MissKm      float64   `json:"miss_km"`
	RelSpeedKmS float64   `json:"rel_speed_km_s"`
	Lat         float64   `json:"lat"`
	Lon         float64   `json:"lon"`
	AltKm       float64   `json:"alt_km"`
}

type summary struct {
	Generated      time.Time      `json:"generated"`
	WindowStart    time.Time      `json:"window_start"`
	WindowHours    float64        `json:"window_hours"`
	Objects        int            `json:"objects"`
	ObjectsByKind  map[string]int `json:"objects_by_kind"`
	Rejected       int            `json:"objects_rejected"`
	ElapsedSeconds float64        `json:"screening_seconds"`
	PairChecks     int64          `json:"pair_checks"`
	Under5Km       int            `json:"approaches_under_5_km"`
	Under1Km       int            `json:"approaches_under_1_km"`
	Under200M      int            `json:"approaches_under_200_m"`
	Formation      int            `json:"slow_pairs_flying_together"`
	Listed         int            `json:"listed"`
	Events         []viewerEvent  `json:"events"`
}

func runFetch(args []string) {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	dir := fs.String("dir", "data/raw", "where the element sets are cached")
	fs.Parse(args)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results, err := fetch.Update(ctx, fetch.CelesTrak, fetch.Options{Dir: *dir, UserAgent: "orbitwatch/0.1 (research)"})
	for _, r := range results {
		switch {
		case r.Fresh:
			log.Printf("%s: downloaded %d element sets", r.Source.Name, r.Objects)
		case r.Err != nil:
			log.Printf("%s: download failed (%v); keeping the cached copy with %d element sets", r.Source.Name, r.Err, r.Objects)
		default:
			log.Printf("%s: cached copy is recent (%d element sets)", r.Source.Name, r.Objects)
		}
	}
	if err != nil {
		log.Fatal(err)
	}
}

type screenConfig struct {
	elements string
	start    time.Time
	hours    float64
	maxAge   time.Duration
	top      int
	out      string
}

func runScreen(args []string) {
	fs := flag.NewFlagSet("screen", flag.ExitOnError)
	cfg := screenConfig{}
	fs.StringVar(&cfg.elements, "elements", "data/raw/gp_*.json", "glob of OMM JSON element files")
	fs.Float64Var(&cfg.hours, "hours", 48, "how far ahead to look")
	startFlag := fs.String("start", "", "window start, RFC 3339; default now")
	fs.DurationVar(&cfg.maxAge, "max-age", 14*24*time.Hour, "ignore element sets older than this")
	fs.IntVar(&cfg.top, "top", 2000, "closest approaches to list for the viewer")
	fs.StringVar(&cfg.out, "out", "web/data", "output directory")
	fs.Parse(args)
	cfg.start = time.Now().UTC().Truncate(time.Minute)
	if *startFlag != "" {
		var err error
		if cfg.start, err = time.Parse(time.RFC3339, *startFlag); err != nil {
			log.Fatalf("bad -start: %v", err)
		}
	}
	if _, _, err := screenOnce(cfg); err != nil {
		log.Fatal(err)
	}
}

// screenOnce loads the cached element sets, screens them and writes the
// viewer's data files. It returns the summary and the objects it used.
func screenOnce(cfg screenConfig) (*summary, []sgp4.Elements, error) {
	paths, _ := filepath.Glob(cfg.elements)
	cat, err := catalog.Load(paths, cfg.start, cfg.maxAge)
	if err != nil {
		return nil, nil, err
	}
	active := map[int]bool{}
	for _, path := range paths {
		if !strings.Contains(filepath.Base(path), "active") {
			continue
		}
		if file, err := os.Open(path); err == nil {
			good, _, _ := catalog.Parse(file)
			file.Close()
			for _, e := range good {
				active[e.NoradID] = true
			}
		}
	}

	window := time.Duration(cfg.hours * float64(time.Hour))
	log.Printf("%d objects (%d rejected), screening %s from %s", len(cat.Objects), len(cat.Rejected), window, cfg.start.Format(time.RFC3339))
	events, stats, err := screen.Run(cat.Objects, screen.Config{Start: cfg.start, Duration: window})
	if err != nil {
		return nil, nil, err
	}
	log.Printf("%d close approaches under 5 km in %s", len(events), stats.Elapsed.Round(time.Second))

	byID := map[int]sgp4.Elements{}
	sum := summary{
		Generated: time.Now().UTC(), WindowStart: cfg.start, WindowHours: cfg.hours, Objects: len(cat.Objects),
		ObjectsByKind: map[string]int{}, Rejected: len(cat.Rejected), ElapsedSeconds: stats.Elapsed.Seconds(), PairChecks: stats.PairChecks,
	}
	vc := viewerCatalog{Generated: sum.Generated}
	for _, e := range cat.Objects {
		byID[e.NoradID] = e
		k := kind(e.Name, active[e.NoradID])
		sum.ObjectsByKind[k]++
		vc.ID, vc.Name, vc.Kind = append(vc.ID, e.NoradID), append(vc.Name, e.Name), append(vc.Kind, k)
		vc.Epoch = append(vc.Epoch, float64(e.Epoch.UnixMicro())/1e6)
		vc.N = append(vc.N, e.MeanMotion*2*math.Pi/86400)
		vc.E = append(vc.E, e.Eccentricity)
		rad := math.Pi / 180
		vc.I, vc.RAAN = append(vc.I, e.Inclination*rad), append(vc.RAAN, e.RAAN*rad)
		vc.ArgP, vc.M = append(vc.ArgP, e.ArgPerigee*rad), append(vc.M, e.MeanAnomaly*rad)
	}

	// Pairs that barely move relative to each other are flying together on
	// purpose (docked modules, formations); they are counted, not ranked.
	var flybys []screen.Event
	for _, e := range events {
		if e.RelSpeedKmS < 0.05 {
			sum.Formation++
			continue
		}
		sum.Under5Km++
		if e.MissKm < 1 {
			sum.Under1Km++
		}
		if e.MissKm < 0.2 {
			sum.Under200M++
		}
		flybys = append(flybys, e)
	}
	sort.Slice(flybys, func(i, j int) bool { return flybys[i].MissKm < flybys[j].MissKm })
	for _, e := range flybys[:min(cfg.top, len(flybys))] {
		a, b := byID[e.A], byID[e.B]
		orbit, err := sgp4.FromElements(a, 'i')
		if err != nil {
			continue
		}
		pos, _, err := orbit.AtTime(e.TCA)
		if err != nil {
			continue
		}
		lat, lon, alt := sgp4.Geodetic(pos, e.TCA)
		sum.Events = append(sum.Events, viewerEvent{
			A: e.A, BID: e.B, NameA: a.Name, NameB: b.Name,
			KindA: kind(a.Name, active[e.A]), KindB: kind(b.Name, active[e.B]),
			TCA: e.TCA, MissKm: round(e.MissKm, 4), RelSpeedKmS: round(e.RelSpeedKmS, 3),
			Lat: round(lat, 3), Lon: round(lon, 3), AltKm: round(alt, 1),
		})
	}
	sum.Listed = len(sum.Events)

	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		return nil, nil, err
	}
	// Reference positions from the full model, so the viewer's lighter
	// orbit maths can be checked against it (see web/orbit.test.mjs).
	type sample struct {
		ID int        `json:"id"`
		T  float64    `json:"t"`
		R  [3]float64 `json:"r"`
	}
	var samples []sample
	for i := 0; i < len(cat.Objects); i += max(1, len(cat.Objects)/400) {
		e := cat.Objects[i]
		at := cfg.start.Add(time.Duration(i%40) * time.Hour)
		if orbit, err := sgp4.FromElements(e, 'i'); err == nil {
			if pos, _, err := orbit.AtTime(at); err == nil {
				samples = append(samples, sample{e.NoradID, float64(at.UnixMicro()) / 1e6, pos})
			}
		}
	}
	for name, v := range map[string]any{"check.json": samples, "catalog.json": vc, "conjunctions.json": sum} {
		if err := writeJSON(filepath.Join(cfg.out, name), v); err != nil {
			return nil, nil, err
		}
	}
	log.Printf("wrote %s: %d flybys under 5 km, %d under 1 km, %d under 200 m, %d pairs flying together", cfg.out, sum.Under5Km, sum.Under1Km, sum.Under200M, sum.Formation)
	return &sum, cat.Objects, nil
}

// elementMessage is one element set as announced on the log.
type elementMessage struct {
	NoradID      int       `json:"norad_id"`
	Name         string    `json:"name"`
	Epoch        time.Time `json:"epoch"`
	MeanMotion   float64   `json:"mean_motion_rev_per_day"`
	Eccentricity float64   `json:"eccentricity"`
	Inclination  float64   `json:"inclination_deg"`
	RAAN         float64   `json:"raan_deg"`
	ArgPerigee   float64   `json:"arg_perigee_deg"`
	MeanAnomaly  float64   `json:"mean_anomaly_deg"`
	BStar        float64   `json:"bstar"`
}

func runWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	every := fs.Duration("every", fetch.ProviderInterval, "time between refreshes; downloads never happen more often than every 2 hours")
	dir := fs.String("dir", "data/raw", "where the element sets are cached")
	brokers := fs.String("kafka-brokers", "", "comma-separated Kafka brokers; empty runs without a message log")
	serveAddr := fs.String("serve", "", "also serve the viewer on this address, for example 0.0.0.0:8080")
	webDir := fs.String("web", "web", "directory with the viewer, used with -serve")
	cfg := screenConfig{maxAge: 14 * 24 * time.Hour}
	fs.Float64Var(&cfg.hours, "hours", 48, "how far ahead to look")
	fs.IntVar(&cfg.top, "top", 2000, "closest approaches to list for the viewer")
	fs.StringVar(&cfg.out, "out", "web/data", "output directory")
	fs.Parse(args)
	cfg.elements = filepath.Join(*dir, "gp_*.json")

	// The message log is optional: without it, or if it cannot be set up,
	// the pipeline still refreshes and screens.
	var publisher *pipeline.Publisher
	if *brokers != "" {
		if kafka, err := pipeline.NewKafka(strings.Split(*brokers, ",")); err != nil {
			log.Printf("kafka unavailable, continuing without it: %v", err)
		} else {
			publisher = pipeline.NewPublisher(kafka, 1<<16)
		}
	}

	epochs := map[int]time.Time{}
	cycle := func(ctx context.Context) error {
		results, err := fetch.Update(ctx, fetch.CelesTrak, fetch.Options{Dir: *dir, MinAge: *every, UserAgent: "orbitwatch/0.1 (research)"})
		for _, r := range results {
			if r.Err != nil {
				log.Printf("%s: download failed (%v); using the cached copy with %d element sets", r.Source.Name, r.Err, r.Objects)
			}
		}
		if err != nil {
			return err
		}
		cfg.start = time.Now().UTC().Truncate(time.Minute)
		sum, objects, err := screenOnce(cfg)
		if err != nil {
			return err
		}
		changed := 0
		for _, e := range objects {
			if epochs[e.NoradID].Equal(e.Epoch) {
				continue
			}
			epochs[e.NoradID] = e.Epoch
			changed++
			value, _ := json.Marshal(elementMessage{e.NoradID, e.Name, e.Epoch, e.MeanMotion, e.Eccentricity, e.Inclination, e.RAAN, e.ArgPerigee, e.MeanAnomaly, e.BStar})
			publisher.Publish(pipeline.Message{Topic: "orbit.elements", Key: []byte(strconv.Itoa(e.NoradID)), Value: value})
		}
		for _, event := range sum.Events[:min(200, len(sum.Events))] {
			value, _ := json.Marshal(event)
			publisher.Publish(pipeline.Message{Topic: "orbit.conjunctions", Key: []byte(fmt.Sprintf("%d-%d", event.A, event.BID)), Value: value})
		}
		log.Printf("cycle done: %d objects (%d with new element sets), %d passes under 1 km, messages %+v", sum.Objects, changed, sum.Under1Km, publisher.Counters())
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *serveAddr != "" {
		srv := viewerServer(*serveAddr, *webDir)
		go func() {
			log.Printf("viewer on http://%s", *serveAddr)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				log.Printf("viewer stopped: %v", err)
			}
		}()
		defer srv.Close()
	}
	pipeline.Loop(ctx, *every, cycle, func(err error) { log.Printf("cycle failed, will retry at the next tick: %v", err) })
	flush, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := publisher.Close(flush); err != nil {
		log.Printf("message log did not drain: %v", err)
	}
	log.Printf("stopped; messages %+v", publisher.Counters())
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

// writeJSON writes to a temporary file first so the viewer never reads a
// half-written file.
func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// viewerServer serves the static viewer. It only reads files below dir.
func viewerServer(addr, dir string) *http.Server {
	files := http.FileServer(http.Dir(dir))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/data/") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10,
	}
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	dir := fs.String("dir", "web", "directory with the viewer")
	fs.Parse(args)
	log.Printf("viewer on http://%s", *addr)
	log.Fatal(viewerServer(*addr, *dir).ListenAndServe())
}
