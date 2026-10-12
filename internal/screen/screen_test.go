package screen

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/shaikn6/orbitwatch/internal/sgp4"
)

var start = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

// crowd makes n low orbits packed into one thin shell, so that close
// approaches are common enough to test with.
func crowd(n int, seed uint64) []sgp4.Elements {
	rng := rand.New(rand.NewPCG(seed, 99))
	out := make([]sgp4.Elements, n)
	for i := range out {
		out[i] = sgp4.Elements{
			NoradID: 1000 + i, Epoch: start.Add(-time.Duration(rng.Float64()*48) * time.Hour),
			MeanMotion: 15.05 + rng.Float64()*0.1, Eccentricity: rng.Float64() * 0.002,
			Inclination: 40 + rng.Float64()*60, RAAN: rng.Float64() * 360,
			ArgPerigee: rng.Float64() * 360, MeanAnomaly: rng.Float64() * 360, BStar: 1e-4,
		}
	}
	return out
}

// bruteForce finds close approaches the slow, obviously correct way: sample
// every pair every second, then refine each dip in distance.
func bruteForce(t *testing.T, objects []sgp4.Elements, window time.Duration, threshold float64) []Event {
	t.Helper()
	w, err := newWorker(objects, Config{Start: start, ThresholdKm: threshold})
	if err != nil {
		t.Fatal(err)
	}
	seconds := int(window.Seconds())
	track := make([][][3]float64, len(objects))
	for i, o := range w.orbits {
		track[i] = make([][3]float64, seconds+1)
		for s := range track[i] {
			p, _, err := o.At(start.Add(time.Duration(s) * time.Second).Sub(w.epoch[i]).Minutes())
			if err != nil {
				t.Fatal(err)
			}
			track[i][s] = p
		}
	}
	dist := func(i, j, s int) float64 {
		d := sub(track[i][s], track[j][s])
		return math.Sqrt(dot(d, d))
	}
	var events []Event
	for i := range objects {
		for j := i + 1; j < len(objects); j++ {
			for s := 1; s < seconds; s++ {
				d := dist(i, j, s)
				if d > threshold+1 || d >= dist(i, j, s-1) || d > dist(i, j, s+1) {
					continue
				}
				at := start.Add(time.Duration(s) * time.Second)
				offset, miss, _ := w.refine(i, j, at, -1, 1)
				if miss < threshold {
					events = append(events, Event{A: w.ids[i], B: w.ids[j], TCA: at.Add(time.Duration(offset * float64(time.Second))), MissKm: miss})
				}
			}
		}
	}
	return merge(events, 30*time.Second)
}

func same(t *testing.T, label string, got, want []Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d events, want %d", label, len(got), len(want))
	}
	for i := 0; i < min(len(got), len(want)); i++ {
		g, w := got[i], want[i]
		if g.A != w.A || g.B != w.B || math.Abs(g.TCA.Sub(w.TCA).Seconds()) > 0.01 || math.Abs(g.MissKm-w.MissKm) > 0.001 {
			t.Fatalf("%s: event %d is %d-%d at %s miss %.4f km, want %d-%d at %s miss %.4f km",
				label, i, g.A, g.B, g.TCA.Format("15:04:05.000"), g.MissKm, w.A, w.B, w.TCA.Format("15:04:05.000"), w.MissKm)
		}
	}
}

func TestFindsExactlyWhatBruteForceFinds(t *testing.T) {
	objects := crowd(220, 1)
	window, threshold := 100*time.Minute, 40.0
	want := bruteForce(t, objects, window, threshold)
	if len(want) < 20 {
		t.Fatalf("brute force found only %d close approaches; the test needs more to mean anything", len(want))
	}
	got, stats, err := Run(objects, Config{Start: start, Duration: window, ThresholdKm: threshold})
	if err != nil {
		t.Fatal(err)
	}
	same(t, "screen vs brute force", got, want)
	all := int64(len(objects)) * int64(len(objects)-1) / 2 * int64(stats.Steps)
	t.Logf("%d close approaches; %d pair checks instead of %d (%.2f%%); %d refined", len(got), stats.PairChecks, all, 100*float64(stats.PairChecks)/float64(all), stats.Refined)
	if stats.PairChecks*3 > all {
		t.Errorf("the grid removed too little work: %d of %d pair checks", stats.PairChecks, all)
	}
}

func TestResultDoesNotDependOnWorkersStepOrChunking(t *testing.T) {
	objects := crowd(160, 2)
	base := Config{Start: start, Duration: 90 * time.Minute, ThresholdKm: 40, Workers: 1, Chunk: 90 * time.Minute}
	want, _, err := Run(objects, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) < 10 {
		t.Fatalf("only %d events", len(want))
	}
	for label, change := range map[string]func(*Config){
		"eight workers":     func(c *Config) { c.Workers = 8; c.Chunk = 7 * time.Minute },
		"one-minute chunks": func(c *Config) { c.Chunk = time.Minute },
		"five-second steps": func(c *Config) { c.Step = 5 * time.Second },
		"forty-second step": func(c *Config) { c.Step = 40 * time.Second },
	} {
		cfg := base
		change(&cfg)
		got, _, err := Run(objects, cfg)
		if err != nil {
			t.Fatal(err)
		}
		same(t, label, got, want)
	}
}

// Two objects in the same orbit, one a few kilometres behind the other,
// never fly past each other. Their distance dips once per revolution, so a
// three-hour window holds two dips, not one event per time step.
func TestCoOrbitingPairGivesOneEventPerRevolution(t *testing.T) {
	lead := sgp4.Elements{NoradID: 1, Epoch: start, MeanMotion: 15.2, Eccentricity: 0.0003, Inclination: 53, RAAN: 10, ArgPerigee: 20, MeanAnomaly: 30, BStar: 1e-4}
	trail := lead
	trail.NoradID, trail.MeanAnomaly = 2, 30-0.02 // about 2.4 km behind
	far := lead
	far.NoradID, far.MeanAnomaly = 3, 210
	events, _, err := Run([]sgp4.Elements{lead, trail, far}, Config{Start: start, Duration: 3 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("%d events, want 2 (one per revolution): %+v", len(events), events)
	}
	period := 24 * time.Hour / 152 * 10
	for _, e := range events {
		if e.A != 1 || e.B != 2 || e.MissKm > 3 || e.RelSpeedKmS > 0.05 {
			t.Fatalf("unexpected event %+v", e)
		}
	}
	if gap := events[1].TCA.Sub(events[0].TCA); gap < period-2*time.Minute || gap > period+2*time.Minute {
		t.Fatalf("dips are %s apart, want about one revolution (%s)", gap, period)
	}
}

func TestDecayedObjectIsCountedAndDoesNotStopTheRun(t *testing.T) {
	objects := crowd(40, 3)
	// Perigee far below the surface a few hours in: the model gives up on it.
	objects[5] = sgp4.Elements{NoradID: 5, Epoch: start, MeanMotion: 16.4, Eccentricity: 0.001, Inclination: 51, BStar: 0.5}
	_, stats, err := Run(objects, Config{Start: start, Duration: 72 * time.Hour, ThresholdKm: 20, Step: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Lost != 1 {
		t.Fatalf("lost = %d, want exactly the decaying object", stats.Lost)
	}
}
