// Package catalog reads published orbital element sets and turns them into
// propagators. It merges several sources, keeps the newest element set per
// object and sets aside anything the orbit model cannot use, with the reason.
package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"github.com/shaikn6/orbitwatch/internal/sgp4"
)

// omm is one element set in the CCSDS OMM JSON form that CelesTrak and
// Space-Track publish.
type omm struct {
	Name         string  `json:"OBJECT_NAME"`
	ID           string  `json:"OBJECT_ID"`
	Epoch        string  `json:"EPOCH"`
	MeanMotion   float64 `json:"MEAN_MOTION"`
	Eccentricity float64 `json:"ECCENTRICITY"`
	Inclination  float64 `json:"INCLINATION"`
	RAAN         float64 `json:"RA_OF_ASC_NODE"`
	ArgPerigee   float64 `json:"ARG_OF_PERICENTER"`
	MeanAnomaly  float64 `json:"MEAN_ANOMALY"`
	NoradID      int     `json:"NORAD_CAT_ID"`
	BStar        float64 `json:"BSTAR"`
}

// Rejected is an element set that was left out, and why.
type Rejected struct {
	NoradID int
	Name    string
	Reason  string
}

// Catalog is the usable objects, ordered by catalog number.
type Catalog struct {
	Objects  []sgp4.Elements
	Rejected []Rejected
}

const epochLayout = "2006-01-02T15:04:05.999999"

// futureSlack allows for element sets issued slightly ahead of time.
const futureSlack = 6 * time.Hour

// maxElementSets bounds one document; the whole public catalog is about
// 30,000 objects.
const maxElementSets = 500_000

// maxFileBytes bounds one element file on disk.
const maxFileBytes = 256 << 20

// sameOrbit reports whether two element sets can describe one object. All
// of the following must hold:
//
//   - the orbit's size, shape and tilt are nearly the same (manoeuvres
//     change them by fractions of a percent and of a degree);
//   - where the older set predicts the object to be at the newer set's
//     epoch is close to where the newer set says it is;
//   - it is moving in nearly the same direction at nearly the same speed
//     there, so the two cannot be different orbits that merely cross.
//
// The position allowance grows with the time between the two sets, because
// stale elements drift, but it is capped: a gap too long to judge is not a
// reason to accept anything.
func sameOrbit(a, b sgp4.Elements) bool {
	if math.Abs(a.MeanMotion-b.MeanMotion) > 0.05*a.MeanMotion ||
		math.Abs(a.Inclination-b.Inclination) > 0.5 ||
		math.Abs(a.Eccentricity-b.Eccentricity) > 0.05 {
		return false
	}
	older, newer := a, b
	if newer.Epoch.Before(older.Epoch) {
		older, newer = newer, older
	}
	from, err := sgp4.FromElements(older, 'i')
	if err != nil {
		return false
	}
	to, err := sgp4.FromElements(newer, 'i')
	if err != nil {
		return false
	}
	gapDays := newer.Epoch.Sub(older.Epoch).Hours() / 24
	allowedKm := math.Min(100+100*gapDays, maxDriftKm)
	// Compared at the newer epoch and again two days on. The second look
	// catches a set that agrees today but is built to part company later,
	// for instance through a made-up drag term.
	for _, ahead := range []time.Duration{0, 48 * time.Hour} {
		at := newer.Epoch.Add(ahead)
		p1, v1, err := from.AtTime(at)
		if err != nil {
			return false
		}
		p2, v2, err := to.AtTime(at)
		if err != nil {
			return false
		}
		var apart, speedGap float64
		for k := range 3 {
			apart += sq(p1[k] - p2[k])
			speedGap += sq(v1[k] - v2[k])
		}
		allowed := allowedKm + 100*ahead.Hours()/24
		if math.Sqrt(apart) > allowed || math.Sqrt(speedGap) > maxSpeedGapKmS {
			return false
		}
	}
	return true
}

const (
	maxDriftKm     = 1000 // largest position disagreement ever accepted
	maxSpeedGapKmS = 0.3  // a change of heading of about two degrees in low orbit
)

func sq(x float64) float64 { return x * x }

// Parse reads one OMM JSON document. It decodes one element set at a time,
// so an oversized document is refused before it is held in memory.
func Parse(r io.Reader) ([]sgp4.Elements, []Rejected, error) {
	dec := json.NewDecoder(r)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil, nil, fmt.Errorf("not an OMM JSON array")
	}
	var good []sgp4.Elements
	var bad []Rejected
	for count := 0; dec.More(); count++ {
		if count >= maxElementSets {
			return nil, nil, fmt.Errorf("document holds more than %d element sets", maxElementSets)
		}
		var o omm
		if err := dec.Decode(&o); err != nil {
			return nil, nil, fmt.Errorf("not an OMM JSON array: %w", err)
		}
		epoch, err := time.Parse(epochLayout, o.Epoch)
		if err != nil {
			bad = append(bad, Rejected{o.NoradID, o.Name, "unreadable epoch " + o.Epoch})
			continue
		}
		e := sgp4.Elements{
			NoradID: o.NoradID, Name: o.Name, Epoch: epoch.UTC(),
			MeanMotion: o.MeanMotion, Eccentricity: o.Eccentricity, Inclination: o.Inclination,
			RAAN: o.RAAN, ArgPerigee: o.ArgPerigee, MeanAnomaly: o.MeanAnomaly, BStar: o.BStar,
		}
		if o.NoradID <= 0 {
			bad = append(bad, Rejected{o.NoradID, o.Name, "no catalog number"})
			continue
		}
		if _, err := sgp4.FromElements(e, 'i'); err != nil {
			bad = append(bad, Rejected{o.NoradID, o.Name, err.Error()})
			continue
		}
		good = append(good, e)
	}
	if _, err := dec.Token(); err != nil {
		return nil, nil, fmt.Errorf("not an OMM JSON array: %w", err)
	}
	return good, bad, nil
}

// Load merges the given files, which should be listed most trusted first.
// When an object appears more than once, the element set with the latest
// epoch wins, but only if it still describes the same orbit: a later source
// that puts an object somewhere completely different is treated as wrong,
// not as news, and the earlier one is kept. Element sets older than maxAge at
// the time now are rejected, because an old element set predicts a position
// that can be tens of kilometres wrong. A file that cannot be read is an
// error only if no file could be read at all, so one bad source does not
// stop the others.
func Load(paths []string, now time.Time, maxAge time.Duration) (*Catalog, error) {
	newest := map[int]sgp4.Elements{}
	// The first element set seen for an object, from the most trusted
	// source that has it. Every later candidate is judged against this
	// one, not against the last one accepted, so a run of small steps
	// cannot walk an object away from where the trusted source put it.
	anchor := map[int]sgp4.Elements{}
	c := &Catalog{}
	var firstErr error
	read := 0
	for _, path := range paths {
		file, err := os.Open(path)
		if err == nil {
			var good []sgp4.Elements
			var bad []Rejected
			good, bad, err = Parse(io.LimitReader(file, maxFileBytes))
			file.Close()
			if err == nil {
				read++
				c.Rejected = append(c.Rejected, bad...)
				for _, e := range good {
					// "Newest wins" must not let a wrong or hostile source
					// displace good data by claiming an epoch in the future.
					if e.Epoch.After(now.Add(futureSlack)) {
						c.Rejected = append(c.Rejected, Rejected{e.NoradID, e.Name, "epoch is in the future"})
						continue
					}
					old, seen := newest[e.NoradID]
					switch {
					case !seen:
						newest[e.NoradID], anchor[e.NoradID] = e, e
					case !sameOrbit(anchor[e.NoradID], e):
						c.Rejected = append(c.Rejected, Rejected{e.NoradID, e.Name, "conflicts with a more trusted source"})
					case e.Epoch.After(old.Epoch):
						newest[e.NoradID] = e
					}
				}
				continue
			}
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", path, err)
		}
	}
	if read == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("no element files given")
		}
		return nil, firstErr
	}
	for _, e := range newest {
		if age := now.Sub(e.Epoch); age > maxAge {
			c.Rejected = append(c.Rejected, Rejected{e.NoradID, e.Name, fmt.Sprintf("element set is %.0f days old", age.Hours()/24)})
			continue
		}
		c.Objects = append(c.Objects, e)
	}
	sort.Slice(c.Objects, func(i, j int) bool { return c.Objects[i].NoradID < c.Objects[j].NoradID })
	return c, nil
}
