// Command validate screens the catalog over the window of a CelesTrak
// SOCRATES report and compares the close approaches found with the ones the
// report lists.
//
// SOCRATES is an independent implementation run on the same public element
// sets, so agreement with it is evidence that the propagator and the
// screening are right. The two can only be expected to agree exactly where
// both used the same element set for both objects, so that subset is
// reported separately from the rest.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/shaikn6/orbitwatch/internal/catalog"
	"github.com/shaikn6/orbitwatch/internal/screen"
)

type reference struct {
	a, b     int
	tca      time.Time
	missKm   float64
	relSpeed float64
	epochA   time.Time // element epochs the report used, from its days-since-epoch columns
	epochB   time.Time
}

type pair struct{ a, b int }

func readReference(path string) ([]reference, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("%s has no events", path)
	}
	col := map[string]int{}
	for i, name := range rows[0] {
		col[name] = i
	}
	for _, name := range []string{"NORAD_CAT_ID_1", "NORAD_CAT_ID_2", "DSE_1", "DSE_2", "TCA", "TCA_RANGE", "TCA_RELATIVE_SPEED"} {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("%s has no %s column", path, name)
		}
	}
	var out []reference
	for _, row := range rows[1:] {
		num := func(name string) float64 {
			v, _ := strconv.ParseFloat(row[col[name]], 64)
			return v
		}
		tca, err := time.Parse("2006-01-02 15:04:05.000", row[col["TCA"]])
		if err != nil {
			return nil, fmt.Errorf("unreadable TCA %q", row[col["TCA"]])
		}
		r := reference{
			a: int(num("NORAD_CAT_ID_1")), b: int(num("NORAD_CAT_ID_2")), tca: tca,
			missKm: num("TCA_RANGE"), relSpeed: num("TCA_RELATIVE_SPEED"),
		}
		r.epochA = tca.Add(-time.Duration(num("DSE_1") * 24 * float64(time.Hour)))
		r.epochB = tca.Add(-time.Duration(num("DSE_2") * 24 * float64(time.Hour)))
		if r.a > r.b {
			r.a, r.b, r.epochA, r.epochB = r.b, r.a, r.epochB, r.epochA
		}
		out = append(out, r)
	}
	return out, nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	return sorted[int(float64(len(sorted)-1)*p)]
}

type spread struct {
	Median float64 `json:"median"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
}

func summarize(values []float64) spread {
	sort.Float64s(values)
	return spread{percentile(values, 0.5), percentile(values, 0.95), percentile(values, 0.99), percentile(values, 1)}
}

type comparison struct {
	Events      int     `json:"reference_events"`
	Found       int     `json:"found"`
	FoundShare  float64 `json:"found_share"`
	TCASeconds  spread  `json:"tca_difference_seconds"`
	MissMetres  spread  `json:"miss_difference_metres"`
	SpeedMetres spread  `json:"relative_speed_difference_m_s"`
}

func main() {
	elements := flag.String("elements", "data/raw/gp_*.json", "glob of OMM JSON element files")
	report := flag.String("report", "data/raw/socrates_minrange.csv", "SOCRATES CSV")
	maxAge := flag.Duration("max-age", 45*24*time.Hour, "ignore element sets older than this")
	step := flag.Duration("step", 15*time.Second, "screening step")
	out := flag.String("out", "runs/validation.json", "where to write the summary")
	flag.Parse()

	refs, err := readReference(*report)
	if err != nil {
		log.Fatal(err)
	}
	first, last := refs[0].tca, refs[0].tca
	for _, r := range refs {
		if r.tca.Before(first) {
			first = r.tca
		}
		if r.tca.After(last) {
			last = r.tca
		}
	}
	paths, _ := filepath.Glob(*elements)
	cat, err := catalog.Load(paths, last, *maxAge+7*24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	epoch := map[int]time.Time{}
	for _, e := range cat.Objects {
		epoch[e.NoradID] = e.Epoch
	}
	start := first.Truncate(time.Hour)
	window := last.Sub(start) + time.Minute
	log.Printf("%d objects (%d rejected), %d reference events, window %s from %s", len(cat.Objects), len(cat.Rejected), len(refs), window.Round(time.Hour), start.Format(time.RFC3339))

	events, stats, err := screen.Run(cat.Objects, screen.Config{
		Start: start, Duration: window, ThresholdKm: 5, Step: *step,
		Progress: func(done, total int) {
			if done%48 == 0 || done == total {
				log.Printf("screened %d/%d chunks", done, total)
			}
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("found %d close approaches in %s (%d pair checks, %d refined, %d objects lost)", len(events), stats.Elapsed.Round(time.Second), stats.PairChecks, stats.Refined, stats.Lost)

	ours := map[pair][]screen.Event{}
	for _, e := range events {
		ours[pair{e.A, e.B}] = append(ours[pair{e.A, e.B}], e)
	}

	// The report gives each object's element age to a thousandth of a day,
	// so "same element set" means the implied epoch is within that rounding.
	const sameEpoch = 3 * time.Minute
	groups := map[string]*struct {
		c                comparison
		tca, miss, speed []float64
	}{"same_element_sets": {}, "different_element_sets": {}}
	var missing int
	for _, r := range refs {
		ea, okA := epoch[r.a]
		eb, okB := epoch[r.b]
		if !okA || !okB {
			missing++
			continue
		}
		name := "different_element_sets"
		if absDuration(ea.Sub(r.epochA)) < sameEpoch && absDuration(eb.Sub(r.epochB)) < sameEpoch {
			name = "same_element_sets"
		}
		g := groups[name]
		g.c.Events++
		var best *screen.Event
		for i, e := range ours[pair{r.a, r.b}] {
			if d := absDuration(e.TCA.Sub(r.tca)); d < 2*time.Minute && (best == nil || d < absDuration(best.TCA.Sub(r.tca))) {
				best = &ours[pair{r.a, r.b}][i]
			}
		}
		if best == nil {
			continue
		}
		g.c.Found++
		g.tca = append(g.tca, absDuration(best.TCA.Sub(r.tca)).Seconds())
		g.miss = append(g.miss, math.Abs(best.MissKm-r.missKm)*1000)
		g.speed = append(g.speed, math.Abs(best.RelSpeedKmS-r.relSpeed)*1000)
	}
	summary := map[string]any{
		"objects": len(cat.Objects), "rejected": len(cat.Rejected), "window_hours": window.Hours(),
		"step_seconds": step.Seconds(), "our_events": len(events), "elapsed_seconds": stats.Elapsed.Seconds(),
		"pair_checks": stats.PairChecks, "refined": stats.Refined, "objects_lost": stats.Lost,
		"reference_events": len(refs), "reference_events_with_an_object_we_lack": missing,
	}
	for name, g := range groups {
		g.c.FoundShare = float64(g.c.Found) / math.Max(1, float64(g.c.Events))
		g.c.TCASeconds, g.c.MissMetres, g.c.SpeedMetres = summarize(g.tca), summarize(g.miss), summarize(g.speed)
		summary[name] = g.c
		log.Printf("%s: found %d of %d (%.2f%%); TCA median %.3f s, p99 %.3f s; miss median %.1f m, p99 %.1f m",
			name, g.c.Found, g.c.Events, 100*g.c.FoundShare, g.c.TCASeconds.Median, g.c.TCASeconds.P99, g.c.MissMetres.Median, g.c.MissMetres.P99)
	}
	data, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		log.Fatal(err)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
