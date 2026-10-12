package catalog

import (
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
	older := write(t, "a.json", `[{"OBJECT_NAME":"ISS OLD","EPOCH":"2026-10-10T00:00:00","MEAN_MOTION":15.5,"ECCENTRICITY":0.0004,"INCLINATION":51.6,"NORAD_CAT_ID":25544},
{"OBJECT_NAME":"STALE","EPOCH":"2026-08-01T00:00:00","MEAN_MOTION":14.0,"ECCENTRICITY":0.001,"INCLINATION":98,"NORAD_CAT_ID":900}]`)
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
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	trusted := write(t, "trusted.json", `[{"OBJECT_NAME":"ISS (ZARYA)","EPOCH":"2026-10-12T06:00:00","MEAN_MOTION":15.50,"ECCENTRICITY":0.0004,"INCLINATION":51.64,"NORAD_CAT_ID":25544}]`)
	update := write(t, "update.json", `[{"OBJECT_NAME":"ISS (ZARYA)","EPOCH":"2026-10-12T10:00:00","MEAN_MOTION":15.51,"ECCENTRICITY":0.0005,"INCLINATION":51.64,"NORAD_CAT_ID":25544}]`)
	forged := write(t, "forged.json", `[{"OBJECT_NAME":"ISS (ZARYA)","EPOCH":"2026-10-12T11:00:00","MEAN_MOTION":14.20,"ECCENTRICITY":0.0005,"INCLINATION":98.0,"NORAD_CAT_ID":25544}]`)

	c, err := Load([]string{trusted, forged}, now, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Objects) != 1 || c.Objects[0].Inclination != 51.64 || len(c.Rejected) != 1 || !strings.Contains(c.Rejected[0].Reason, "conflicts") {
		t.Fatalf("a conflicting later source displaced trusted data: %+v, rejected %+v", c.Objects, c.Rejected)
	}
	c, _ = Load([]string{trusted, update}, now, 14*24*time.Hour)
	if len(c.Objects) != 1 || c.Objects[0].MeanMotion != 15.51 {
		t.Fatalf("a genuine update was not taken: %+v", c.Objects)
	}
}
