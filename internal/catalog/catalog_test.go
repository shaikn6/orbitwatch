package catalog

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaikn6/orbitwatch/internal/sgp4"
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

func TestLoadMergesSourcesKeepsNewestAndDropsStale(t *testing.T) {
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
	if len(c.Objects) != 2 || c.Objects[0].Name != "ISS (ZARYA)" || c.Objects[1].NoradID != 100537 {
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

func TestALaterSourceCannotMoveAnObjectToADifferentOrbit(t *testing.T) {
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	load := func(second omm) *Catalog {
		t.Helper()
		c, err := Load([]string{write(t, "trusted.json", document(iss)), write(t, "second.json", document(second))}, now, 14*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	genuine := later(iss, 4)
	genuine.MeanMotion += 0.001 // a small reboost
	if c := load(genuine); len(c.Objects) != 1 || c.Objects[0].MeanMotion != genuine.MeanMotion || len(c.Rejected) != 0 {
		t.Fatalf("a genuine update was not taken: %+v, rejected %+v", c.Objects, c.Rejected)
	}

	forgeries := map[string]func(*omm){
		"different size and tilt": func(o *omm) { o.MeanMotion, o.Inclination = 14.2, 98 },
		// The fields a field-by-field comparison would look at all match.
		"half an orbit ahead": func(o *omm) { o.MeanAnomaly = math.Mod(o.MeanAnomaly+180, 360) },
		"orbit plane turned":  func(o *omm) { o.RAAN = math.Mod(o.RAAN+90, 360) },
	}
	for name, forge := range forgeries {
		forged := later(iss, 4)
		forge(&forged)
		c := load(forged)
		if len(c.Objects) != 1 || c.Objects[0].Epoch.Format(epochLayout) != iss.Epoch {
			t.Errorf("%s: the forged set displaced the trusted one: %+v", name, c.Objects)
		}
		if len(c.Rejected) != 1 || !strings.Contains(c.Rejected[0].Reason, "conflicts") {
			t.Errorf("%s: rejected = %+v", name, c.Rejected)
		}
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

func TestSameOrbitAllowanceIsCappedAndChecksDirectionOfTravel(t *testing.T) {
	parse := func(o omm) sgp4.Elements {
		good, _, err := Parse(strings.NewReader(document(o)))
		if err != nil || len(good) != 1 {
			t.Fatalf("fixture did not parse: %v", err)
		}
		return good[0]
	}
	// Without drag, so that the simple drift in later() matches the model
	// over a gap this long.
	calm := iss
	calm.BStar = 0
	base := parse(calm)
	if !sameOrbit(base, parse(later(calm, 24*10))) {
		t.Fatal("an honest update ten days later was rejected")
	}
	// Ten days on and 1,500 km (about 12.7 degrees) along the orbit from
	// where it should be: inside an uncapped allowance, outside the cap.
	shifted := later(calm, 24*10)
	shifted.MeanAnomaly = math.Mod(shifted.MeanAnomaly+12.7, 360)
	if sameOrbit(base, parse(shifted)) {
		t.Fatal("a set 1,500 km out of place was accepted because the gap was long")
	}
	// A tilt change no manoeuvre makes between two updates.
	tilted := later(calm, 1)
	tilted.Inclination += 1.0
	if sameOrbit(base, parse(tilted)) {
		t.Fatal("a set tilted by a whole degree was accepted")
	}
	// The check does not depend on which set is passed first.
	if sameOrbit(parse(shifted), base) || !sameOrbit(parse(later(calm, 6)), base) {
		t.Fatal("the result depends on argument order")
	}
}
