package catalog

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `[
{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","EPOCH":"2026-10-12T08:15:30.123456","MEAN_MOTION":15.5,"ECCENTRICITY":0.0004,"INCLINATION":51.63,"RA_OF_ASC_NODE":120.1,"ARG_OF_PERICENTER":80.2,"MEAN_ANOMALY":279.9,"NORAD_CAT_ID":25544,"BSTAR":0.00021},
{"OBJECT_NAME":"SIX DIGIT","OBJECT_ID":"2026-001A","EPOCH":"2026-10-11T00:00:00","MEAN_MOTION":15.06,"ECCENTRICITY":0.0001,"INCLINATION":53.0,"RA_OF_ASC_NODE":1,"ARG_OF_PERICENTER":2,"MEAN_ANOMALY":3,"NORAD_CAT_ID":100537,"BSTAR":0.0001},
{"OBJECT_NAME":"BROKEN","OBJECT_ID":"x","EPOCH":"2026-10-11T00:00:00","MEAN_MOTION":0,"ECCENTRICITY":0.1,"INCLINATION":10,"NORAD_CAT_ID":7,"BSTAR":0},
{"OBJECT_NAME":"BAD EPOCH","OBJECT_ID":"y","EPOCH":"yesterday","MEAN_MOTION":14,"ECCENTRICITY":0.1,"INCLINATION":10,"NORAD_CAT_ID":8,"BSTAR":0}
]`

// later returns the element set a tracking network would publish for the
// same object some hours on: same orbit, with the object moved along it and
// the orbit's orientation drifted the way the Earth's bulge makes it drift.
func later(o omm, hours float64) omm {
	const mu, radius, j2 = 398600.4418, 6378.137, 1.08262668e-3
	epoch, _ := time.Parse(epochLayout, o.Epoch)
	dt := hours * 3600
	n := o.MeanMotion * 2 * math.Pi / 86400
	a := math.Cbrt(mu / (n * n))
	k := 1.5 * j2 * math.Pow(radius/(a*(1-o.Eccentricity*o.Eccentricity)), 2) * n
	inc := o.Inclination * math.Pi / 180
	deg := 180 / math.Pi
	wrap := func(x float64) float64 { return math.Mod(math.Mod(x, 360)+360, 360) }
	o.Epoch = epoch.Add(time.Duration(dt * float64(time.Second))).Format(epochLayout)
	o.RAAN = wrap(o.RAAN - k*math.Cos(inc)*dt*deg)
	o.ArgPerigee = wrap(o.ArgPerigee + k*(2-2.5*math.Pow(math.Sin(inc), 2))*dt*deg)
	o.MeanAnomaly = wrap(o.MeanAnomaly + n*dt*deg)
	return o
}

func document(sets ...omm) string {
	data, _ := json.Marshal(sets)
	return string(data)
}

var iss = omm{Name: "ISS (ZARYA)", Epoch: "2026-10-12T08:15:30.123456", MeanMotion: 15.5, Eccentricity: 0.0004, Inclination: 51.63, RAAN: 120.1, ArgPerigee: 80.2, MeanAnomaly: 279.9, NoradID: 25544, BStar: 0.00021}

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseKeepsGoodElementsAndExplainsRejections(t *testing.T) {
	good, bad, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(good) != 2 || good[0].NoradID != 25544 || good[1].NoradID != 100537 {
		t.Fatalf("good = %+v", good)
	}
	if got := good[0].Epoch; got.Nanosecond() != 123456000 || got.Hour() != 8 {
		t.Fatalf("epoch lost precision: %v", got)
	}
	if len(bad) != 2 || !strings.Contains(bad[0].Reason, "mean motion") || !strings.Contains(bad[1].Reason, "epoch") {
		t.Fatalf("rejected = %+v", bad)
	}
	if _, _, err := Parse(strings.NewReader(`{"not":"an array"}`)); err == nil {
		t.Fatal("a non-array document was accepted")
	}
}

func TestLoadMergesSourcesFirstListedWinsAndStaleIsDropped(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	earlier := later(iss, -56)
	earlier.Name = "ISS OLD"
	older := write(t, "a.json", document(earlier, omm{Name: "STALE", Epoch: "2026-08-01T00:00:00", MeanMotion: 14.0, Eccentricity: 0.001, Inclination: 98, NoradID: 900}))
	newer := write(t, "b.json", sample)
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")

	c, err := Load([]string{older, missing, newer}, now, 14*24*time.Hour)
	if err != nil {
		t.Fatalf("one unreadable source must not stop the others: %v", err)
	}
	if len(c.Objects) != 2 || c.Objects[0].Name != "ISS OLD" || c.Objects[1].NoradID != 100537 || c.Shadowed != 1 {
		t.Fatalf("objects = %+v", c.Objects)
	}
	stale := false
	for _, r := range c.Rejected {
		stale = stale || (r.NoradID == 900 && strings.Contains(r.Reason, "days old"))
	}
	if !stale {
		t.Fatalf("stale element set was not rejected: %+v", c.Rejected)
	}
	if _, err := Load([]string{missing}, now, time.Hour); err == nil {
		t.Fatal("Load succeeded with no readable source")
	}
}

func TestAFutureDatedElementSetCannotDisplaceGoodData(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	good := write(t, "good.json", `[{"OBJECT_NAME":"ISS (ZARYA)","EPOCH":"2026-10-12T08:00:00","MEAN_MOTION":15.5,"ECCENTRICITY":0.0004,"INCLINATION":51.6,"NORAD_CAT_ID":25544}]`)
	forged := write(t, "forged.json", `[{"OBJECT_NAME":"ISS FORGED","EPOCH":"2027-01-01T00:00:00","MEAN_MOTION":15.0,"ECCENTRICITY":0.001,"INCLINATION":5,"NORAD_CAT_ID":25544}]`)
	c, err := Load([]string{good, forged}, now, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Objects) != 1 || c.Objects[0].Name != "ISS (ZARYA)" {
		t.Fatalf("objects = %+v, want the genuine element set", c.Objects)
	}
	if len(c.Rejected) != 1 || !strings.Contains(c.Rejected[0].Reason, "future") {
		t.Fatalf("rejected = %+v", c.Rejected)
	}
}

func TestParseStopsAtTheElementSetLimit(t *testing.T) {
	one := `{"OBJECT_NAME":"X","EPOCH":"2026-10-12T00:00:00","MEAN_MOTION":15,"ECCENTRICITY":0.001,"INCLINATION":50,"NORAD_CAT_ID":5}`
	huge := "[" + strings.Repeat(one+",", maxElementSets) + one + "]"
	if _, _, err := Parse(strings.NewReader(huge)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("err = %v, want the limit to be enforced", err)
	}
	for _, broken := range []string{`[` + one, `[` + one + `,`, `{}`, ``, `[1,2]`} {
		if _, _, err := Parse(strings.NewReader(broken)); err == nil {
			t.Errorf("Parse(%.20q...) succeeded", broken)
		}
	}
}

func TestALessTrustedSourceCanOnlyFillGaps(t *testing.T) {
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	trusted := write(t, "trusted.json", document(iss))
	extra := omm{Name: "ONLY IN SECOND", Epoch: "2026-10-12T09:00:00", MeanMotion: 14.2, Eccentricity: 0.001, Inclination: 98, NoradID: 43013}

	// Whatever the second source says about an object the first one has,
	// newer, subtly shifted or wildly different, it is not used.
	attempts := map[string]func(*omm){
		"newer and plausible":   func(o *omm) { o.MeanMotion += 0.001 },
		"moved along the orbit": func(o *omm) { o.MeanAnomaly = math.Mod(o.MeanAnomaly+0.4, 360) },
		"forged drag term":      func(o *omm) { o.BStar = 0.05 },
		"different orbit":       func(o *omm) { o.MeanMotion, o.Inclination = 14.2, 98 },
	}
	for name, change := range attempts {
		other := later(iss, 4)
		change(&other)
		c, err := Load([]string{trusted, write(t, "second.json", document(other, extra))}, now, 14*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Objects) != 2 || c.Objects[0].Epoch.Format(epochLayout) != iss.Epoch || c.Objects[0].BStar != iss.BStar {
			t.Errorf("%s: the less trusted source changed a trusted object: %+v", name, c.Objects[0])
		}
		if c.Objects[1].NoradID != 43013 || c.Shadowed != 1 {
			t.Errorf("%s: gap not filled or shadowing not counted: %+v, shadowed %d", name, c.Objects, c.Shadowed)
		}
	}

	// Order is what carries trust: listed first, the other source wins.
	c, _ := Load([]string{write(t, "second.json", document(later(iss, 4))), trusted}, now, 14*24*time.Hour)
	if c.Objects[0].Epoch.Format(epochLayout) == iss.Epoch {
		t.Fatal("the first-listed source did not take precedence")
	}
}

func TestWithinOneFileTheNewestElementSetIsUsed(t *testing.T) {
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	newer := later(iss, 3)
	c, err := Load([]string{write(t, "one.json", document(newer, iss, later(iss, -20)))}, now, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Objects) != 1 || c.Objects[0].Epoch.Format(epochLayout) != newer.Epoch {
		t.Fatalf("objects = %+v, want the newest of the three", c.Objects)
	}
}
