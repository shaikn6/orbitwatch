// Package screen finds close approaches between every pair of objects in a
// catalog over a time window.
//
// Checking every pair at every instant is hopeless: 29,000 objects make 420
// million pairs. Instead time is cut into short steps. At each step every
// object is dropped into a grid of cubes whose side is the farthest two
// objects can be apart at the start of a step and still come within the
// threshold before it ends. Only objects in the same or touching cubes can
// possibly meet, so only those are compared. For such a pair, treating both
// as moving in straight lines for the length of the step gives the moment
// and distance of closest approach in closed form; the few pairs that pass
// are then refined with the real orbit model.
package screen

import (
	"math"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shaikn6/orbitwatch/internal/sgp4"
)

// Event is one close approach.
type Event struct {
	A, B        int       `json:"-"` // catalog numbers, A < B
	TCA         time.Time `json:"tca"`
	MissKm      float64   `json:"miss_km"`
	RelSpeedKmS float64   `json:"rel_speed_km_s"`
}

// Config describes one screening run.
type Config struct {
	Start       time.Time
	Duration    time.Duration
	ThresholdKm float64       // report approaches closer than this; default 5
	Step        time.Duration // default 15 s
	Chunk       time.Duration // work unit handed to one worker; default 30 min
	Workers     int           // default: one per CPU
	Progress    func(done, total int)
}

// Stats says how much work a run did.
type Stats struct {
	Objects    int           `json:"objects"`
	Lost       int           `json:"objects_lost"` // the model stopped giving positions, e.g. after decay
	Steps      int           `json:"steps"`
	PairChecks int64         `json:"pair_checks"`
	Refined    int64         `json:"refined"`
	Elapsed    time.Duration `json:"elapsed"`
}

const (
	// linearSlackKm covers the error of treating two nearby objects as
	// moving in straight lines for one step.
	linearSlackKm = 0.5
	// edgeSlack lets a closest approach that falls just outside a step be
	// claimed by it; the duplicate this can create is merged afterwards.
	edgeSlack = 0.5 // seconds
	// slowKmS marks pairs that drift rather than fly past each other.
	slowKmS = 0.05
)

// Run screens objects against each other. The result is sorted by time and
// does not depend on the number of workers or the chunk length.
func Run(objects []sgp4.Elements, cfg Config) ([]Event, Stats, error) {
	began := time.Now()
	if cfg.ThresholdKm <= 0 {
		cfg.ThresholdKm = 5
	}
	if cfg.Step <= 0 {
		cfg.Step = 15 * time.Second
	}
	if cfg.Chunk <= 0 {
		cfg.Chunk = 30 * time.Minute
	}
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.NumCPU()
	}
	// Chunks hold a whole number of steps so that step times do not depend
	// on how the window is cut up.
	stepsPerChunk := max(1, int(cfg.Chunk/cfg.Step))
	totalSteps := int(math.Ceil(float64(cfg.Duration) / float64(cfg.Step)))
	chunks := (totalSteps + stepsPerChunk - 1) / stepsPerChunk

	stats := Stats{Objects: len(objects), Steps: totalSteps}
	var pairChecks, refined atomic.Int64
	var done atomic.Int64
	lost := make([]atomic.Bool, len(objects))
	results := make([][]Event, chunks)
	next := make(chan int)
	var firstErr error
	var errOnce sync.Once
	var wg sync.WaitGroup

	for range min(cfg.Workers, max(chunks, 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := newWorker(objects, cfg)
			if err != nil {
				errOnce.Do(func() { firstErr = err })
				for range next {
				}
				return
			}
			for c := range next {
				first := c * stepsPerChunk
				count := min(stepsPerChunk, totalSteps-first)
				results[c] = w.chunk(first, count)
				pairChecks.Add(w.pairChecks)
				refined.Add(w.refined)
				w.pairChecks, w.refined = 0, 0
				for i, gone := range w.lost {
					if gone {
						lost[i].Store(true)
					}
				}
				if cfg.Progress != nil {
					cfg.Progress(int(done.Add(1)), chunks)
				}
			}
		}()
	}
	for c := range chunks {
		next <- c
	}
	close(next)
	wg.Wait()
	if firstErr != nil {
		return nil, stats, firstErr
	}

	var all []Event
	for _, part := range results {
		all = append(all, part...)
	}
	events := merge(all, 2*cfg.Step)
	for i := range lost {
		if lost[i].Load() {
			stats.Lost++
		}
	}
	stats.PairChecks, stats.Refined, stats.Elapsed = pairChecks.Load(), refined.Load(), time.Since(began)
	return events, stats, nil
}

// merge removes the duplicates that arise when one approach is claimed by
// two neighbouring steps, and collapses a slow drift into its closest point.
func merge(events []Event, window time.Duration) []Event {
	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.A != b.A {
			return a.A < b.A
		}
		if a.B != b.B {
			return a.B < b.B
		}
		return a.TCA.Before(b.TCA)
	})
	out := events[:0]
	for i := 0; i < len(events); {
		best, last := events[i], events[i].TCA
		j := i + 1
		for ; j < len(events) && events[j].A == best.A && events[j].B == best.B && events[j].TCA.Sub(last) <= window; j++ {
			last = events[j].TCA
			if events[j].MissKm < best.MissKm {
				best = events[j]
			}
		}
		out = append(out, best)
		i = j
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].TCA.Equal(out[j].TCA) {
			return out[i].TCA.Before(out[j].TCA)
		}
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out
}

// worker owns one propagator per object; propagators keep state and cannot
// be shared between goroutines.
type worker struct {
	cfg    Config
	orbits []*sgp4.Orbit
	ids    []int
	epoch  []time.Time
	lost   []bool

	pos, vel   [][3]float64
	cell       [][3]int32
	head, next []int32

	pairChecks, refined int64
}

const buckets = 1 << 16

func newWorker(objects []sgp4.Elements, cfg Config) (*worker, error) {
	n := len(objects)
	w := &worker{
		cfg: cfg, orbits: make([]*sgp4.Orbit, n), ids: make([]int, n), epoch: make([]time.Time, n),
		lost: make([]bool, n), pos: make([][3]float64, n), vel: make([][3]float64, n),
		cell: make([][3]int32, n), head: make([]int32, buckets), next: make([]int32, n),
	}
	for i, e := range objects {
		o, err := sgp4.FromElements(e, 'i')
		if err != nil {
			return nil, err
		}
		w.orbits[i], w.ids[i], w.epoch[i] = o, e.NoradID, o.Epoch
	}
	return w, nil
}

func bucket(c [3]int32) int32 {
	h := uint32(c[0])*73856093 ^ uint32(c[1])*19349663 ^ uint32(c[2])*83492791
	return int32(h & (buckets - 1))
}

// forward lists the neighbouring cubes that come after a cube in scan
// order, so that every touching pair of cubes is visited exactly once.
var forward = func() [][3]int32 {
	var out [][3]int32
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			for dz := int32(-1); dz <= 1; dz++ {
				if dx > 0 || (dx == 0 && dy > 0) || (dx == 0 && dy == 0 && dz > 0) {
					out = append(out, [3]int32{dx, dy, dz})
				}
			}
		}
	}
	return out
}()

func (w *worker) chunk(firstStep, steps int) []Event {
	var events []Event
	dt := w.cfg.Step.Seconds()
	for k := range steps {
		t := w.cfg.Start.Add(time.Duration(firstStep+k) * w.cfg.Step)
		var fastest float64
		for i, o := range w.orbits {
			if w.lost[i] {
				continue
			}
			p, v, err := o.At(t.Sub(w.epoch[i]).Minutes())
			if err != nil {
				w.lost[i] = true
				continue
			}
			w.pos[i], w.vel[i] = p, v
			fastest = math.Max(fastest, v[0]*v[0]+v[1]*v[1]+v[2]*v[2])
		}
		// Two objects closing at twice the top speed cover this much in one
		// step; anything farther apart now cannot meet before the next step.
		reach := w.cfg.ThresholdKm + linearSlackKm + 2*math.Sqrt(fastest)*(dt+2*edgeSlack)
		inv := 1 / reach
		for b := range w.head {
			w.head[b] = -1
		}
		for i := range w.orbits {
			if w.lost[i] {
				continue
			}
			c := [3]int32{int32(math.Floor(w.pos[i][0] * inv)), int32(math.Floor(w.pos[i][1] * inv)), int32(math.Floor(w.pos[i][2] * inv))}
			w.cell[i] = c
			b := bucket(c)
			w.next[i], w.head[b] = w.head[b], int32(i)
		}
		for i := range w.orbits {
			if w.lost[i] {
				continue
			}
			c := w.cell[i]
			// Same cube: each pair once, by index order.
			for j := w.next[i]; j >= 0; j = w.next[j] {
				if w.cell[j] == c {
					events = w.check(events, i, int(j), t, dt, reach)
				}
			}
			for _, d := range forward {
				n := [3]int32{c[0] + d[0], c[1] + d[1], c[2] + d[2]}
				for j := w.head[bucket(n)]; j >= 0; j = w.next[j] {
					if w.cell[j] == n {
						events = w.check(events, i, int(j), t, dt, reach)
					}
				}
			}
		}
	}
	return events
}

// check tests one pair for a closest approach inside the step that starts
// at t, and appends it when the real orbits confirm it.
func (w *worker) check(events []Event, i, j int, t time.Time, dt, reach float64) []Event {
	w.pairChecks++
	r := sub(w.pos[i], w.pos[j])
	if dot(r, r) > reach*reach {
		return events
	}
	v := sub(w.vel[i], w.vel[j])
	speed2 := dot(v, v)
	lo, hi := -edgeSlack, dt+edgeSlack
	if speed2 > slowKmS*slowKmS {
		tau := -dot(r, v) / speed2
		if tau < lo || tau > hi {
			return events
		}
		closest := [3]float64{r[0] + v[0]*tau, r[1] + v[1]*tau, r[2] + v[2]*tau}
		limit := w.cfg.ThresholdKm + linearSlackKm
		if dot(closest, closest) > limit*limit {
			return events
		}
		// A fly-past pins the moment down well; search tightly around it.
		lo, hi = math.Max(lo, tau-2), math.Min(hi, tau+2)
	} else if dot(r, r) > sq(w.cfg.ThresholdKm+linearSlackKm+slowKmS*dt) {
		return events
	}

	w.refined++
	offset, miss, ok := w.refine(i, j, t, lo, hi)
	if !ok || miss >= w.cfg.ThresholdKm {
		return events
	}
	at := t.Add(time.Duration(offset * float64(time.Second)))
	_, vi, _ := w.orbits[i].At(at.Sub(w.epoch[i]).Minutes())
	_, vj, _ := w.orbits[j].At(at.Sub(w.epoch[j]).Minutes())
	rel := sub(vi, vj)
	a, b := w.ids[i], w.ids[j]
	if a > b {
		a, b = b, a
	}
	return append(events, Event{A: a, B: b, TCA: at, MissKm: miss, RelSpeedKmS: math.Sqrt(dot(rel, rel))})
}

// refine finds the true closest approach of a pair between lo and hi
// seconds after t by golden-section search on the real orbit model.
func (w *worker) refine(i, j int, t time.Time, lo, hi float64) (offset, miss float64, ok bool) {
	baseI, baseJ := t.Sub(w.epoch[i]).Minutes(), t.Sub(w.epoch[j]).Minutes()
	distance := func(s float64) float64 {
		pi, _, errI := w.orbits[i].At(baseI + s/60)
		pj, _, errJ := w.orbits[j].At(baseJ + s/60)
		if errI != nil || errJ != nil {
			return math.Inf(1)
		}
		d := sub(pi, pj)
		return math.Sqrt(dot(d, d))
	}
	const ratio = 0.6180339887498949
	a, b := lo, hi
	x1, x2 := b-ratio*(b-a), a+ratio*(b-a)
	f1, f2 := distance(x1), distance(x2)
	for b-a > 1e-4 {
		if f1 < f2 {
			b, x2, f2 = x2, x1, f1
			x1 = b - ratio*(b-a)
			f1 = distance(x1)
		} else {
			a, x1, f1 = x1, x2, f2
			x2 = a + ratio*(b-a)
			f2 = distance(x2)
		}
	}
	offset = (a + b) / 2
	miss = distance(offset)
	return offset, miss, !math.IsInf(miss, 1)
}

func sub(a, b [3]float64) [3]float64 { return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func dot(a, b [3]float64) float64    { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func sq(x float64) float64           { return x * x }
