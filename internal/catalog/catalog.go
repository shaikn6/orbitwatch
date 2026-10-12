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
}

const epochLayout = "2006-01-02T15:04:05.999999"

// futureSlack allows for element sets issued slightly ahead of time.
const futureSlack = 6 * time.Hour

// Parse reads one OMM JSON document.
func Parse(r io.Reader) ([]sgp4.Elements, []Rejected, error) {
	var raw []omm
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("not an OMM JSON array: %w", err)
	}
	var good []sgp4.Elements
	var bad []Rejected
	for _, o := range raw {
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
	return good, bad, nil
}

// Load merges the given files. When an object appears more than once, the
// element set with the latest epoch wins. Element sets older than maxAge at
// the time now are rejected, because an old element set predicts a position
// that can be tens of kilometres wrong. A file that cannot be read is an
// error only if no file could be read at all, so one bad source does not
// stop the others.
func Load(paths []string, now time.Time, maxAge time.Duration) (*Catalog, error) {
	newest := map[int]sgp4.Elements{}
	c := &Catalog{}
	var firstErr error
	read := 0
	for _, path := range paths {
		file, err := os.Open(path)
		if err == nil {
			var good []sgp4.Elements
			var bad []Rejected
			good, bad, err = Parse(file)
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
					if old, ok := newest[e.NoradID]; !ok || e.Epoch.After(old.Epoch) {
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
