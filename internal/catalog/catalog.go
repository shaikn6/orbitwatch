// Package catalog reads published orbital element sets and turns them into
// propagators. It merges several sources, keeps the newest element set per
// object and sets aside anything the orbit model cannot use, with the reason.
package catalog

import (
	"encoding/json"
	"fmt"
	"io"
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
	// Shadowed counts element sets ignored because a more trusted file
	// already had the object.
	Shadowed int
}

const epochLayout = "2006-01-02T15:04:05.999999"

// futureSlack allows for element sets issued slightly ahead of time.
const futureSlack = 6 * time.Hour

// maxElementSets bounds one document; the whole public catalog is about
// 30,000 objects.
const maxElementSets = 500_000

// maxFileBytes bounds one element file on disk.
const maxFileBytes = 256 << 20

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

// Load merges the given files, which must be listed most trusted first.
//
// The rule for an object that appears in more than one file is strict: the
// first file that has it decides, and later files are ignored for that
// object. Later files only add objects the earlier ones lack. This gives up
// some freshness, since a less trusted source may hold a newer element set,
// in exchange for a guarantee that is easy to state and check: no source
// can change what a more trusted source says about an object. Within one
// file the element set with the latest epoch is used.
//
// Element sets dated in the future, and those older than maxAge at the time
// now, are rejected. A file that cannot be read is an error only if no file
// could be read at all, so one bad source does not stop the others.
func Load(paths []string, now time.Time, maxAge time.Duration) (*Catalog, error) {
	chosen := map[int]sgp4.Elements{}
	c := &Catalog{}
	var firstErr error
	read := 0
	for _, path := range paths {
		good, bad, err := parseFile(path)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", path, err)
			}
			continue
		}
		read++
		c.Rejected = append(c.Rejected, bad...)
		inThisFile := map[int]sgp4.Elements{}
		for _, e := range good {
			if e.Epoch.After(now.Add(futureSlack)) {
				c.Rejected = append(c.Rejected, Rejected{e.NoradID, e.Name, "epoch is in the future"})
				continue
			}
			if old, ok := inThisFile[e.NoradID]; !ok || e.Epoch.After(old.Epoch) {
				inThisFile[e.NoradID] = e
			}
		}
		for id, e := range inThisFile {
			if _, taken := chosen[id]; taken {
				c.Shadowed++
				continue
			}
			chosen[id] = e
		}
	}
	if read == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("no element files given")
		}
		return nil, firstErr
	}
	for _, e := range chosen {
		if age := now.Sub(e.Epoch); age > maxAge {
			c.Rejected = append(c.Rejected, Rejected{e.NoradID, e.Name, fmt.Sprintf("element set is %.0f days old", age.Hours()/24)})
			continue
		}
		c.Objects = append(c.Objects, e)
	}
	sort.Slice(c.Objects, func(i, j int) bool { return c.Objects[i].NoradID < c.Objects[j].NoradID })
	return c, nil
}

func parseFile(path string) ([]sgp4.Elements, []Rejected, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	return Parse(io.LimitReader(file, maxFileBytes))
}
