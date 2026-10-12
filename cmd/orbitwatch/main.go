// Command orbitwatch screens the public satellite catalog for close
// approaches and writes the data the globe viewer shows.
//
//	orbitwatch screen   compute upcoming close approaches and write web/data
//	orbitwatch serve    serve the viewer
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaikn6/orbitwatch/internal/catalog"
	"github.com/shaikn6/orbitwatch/internal/screen"
	"github.com/shaikn6/orbitwatch/internal/sgp4"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: orbitwatch screen|serve [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "screen":
		runScreen(os.Args[2:])
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

func runScreen(args []string) {
	fs := flag.NewFlagSet("screen", flag.ExitOnError)
	elements := fs.String("elements", "data/raw/gp_*.json", "glob of OMM JSON element files")
	hours := fs.Float64("hours", 48, "how far ahead to look")
	startFlag := fs.String("start", "", "window start, RFC 3339; default now")
	maxAge := fs.Duration("max-age", 14*24*time.Hour, "ignore element sets older than this")
	top := fs.Int("top", 2000, "closest approaches to list for the viewer")
	out := fs.String("out", "web/data", "output directory")
	fs.Parse(args)

	start := time.Now().UTC().Truncate(time.Minute)
	if *startFlag != "" {
		var err error
		if start, err = time.Parse(time.RFC3339, *startFlag); err != nil {
			log.Fatalf("bad -start: %v", err)
		}
	}
	paths, _ := filepath.Glob(*elements)
	cat, err := catalog.Load(paths, start, *maxAge)
	if err != nil {
		log.Fatal(err)
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

	window := time.Duration(*hours * float64(time.Hour))
	log.Printf("%d objects (%d rejected), screening %s from %s", len(cat.Objects), len(cat.Rejected), window, start.Format(time.RFC3339))
	events, stats, err := screen.Run(cat.Objects, screen.Config{Start: start, Duration: window})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d close approaches under 5 km in %s", len(events), stats.Elapsed.Round(time.Second))

	byID := map[int]sgp4.Elements{}
	sum := summary{
		Generated: time.Now().UTC(), WindowStart: start, WindowHours: *hours, Objects: len(cat.Objects),
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
	for _, e := range flybys[:min(*top, len(flybys))] {
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

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
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
		at := start.Add(time.Duration(i%40) * time.Hour)
		if orbit, err := sgp4.FromElements(e, 'i'); err == nil {
			if pos, _, err := orbit.AtTime(at); err == nil {
				samples = append(samples, sample{e.NoradID, float64(at.UnixMicro()) / 1e6, pos})
			}
		}
	}
	writeJSON(filepath.Join(*out, "check.json"), samples)
	writeJSON(filepath.Join(*out, "catalog.json"), vc)
	writeJSON(filepath.Join(*out, "conjunctions.json"), sum)
	log.Printf("wrote %s: %d flybys under 5 km, %d under 1 km, %d under 200 m, %d pairs flying together", *out, sum.Under5Km, sum.Under1Km, sum.Under200M, sum.Formation)
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

// writeJSON writes to a temporary file first so the viewer never reads a
// half-written file.
func writeJSON(path string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Fatal(err)
	}
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	dir := fs.String("dir", "web", "directory with the viewer")
	fs.Parse(args)
	files := http.FileServer(http.Dir(*dir))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/data/") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("viewer on http://%s", *addr)
	log.Fatal(srv.ListenAndServe())
}
