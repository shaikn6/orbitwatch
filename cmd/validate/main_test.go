package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCSV writes lines to a temporary file and returns its path.
func writeCSV(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.csv")
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const header = "NORAD_CAT_ID_1,NORAD_CAT_ID_2,DSE_1,DSE_2,TCA,TCA_RANGE,TCA_RELATIVE_SPEED"

func TestReadReference(t *testing.T) {
	tca := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	path := writeCSV(t, header,
		"100,200,1.5,0.5,2026-11-06 12:00:00.000,0.842,7.512")
	refs, err := readReference(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d rows, want 1", len(refs))
	}
	r := refs[0]
	if r.a != 100 || r.b != 200 {
		t.Errorf("ids = %d, %d; want 100, 200", r.a, r.b)
	}
	if !r.tca.Equal(tca) {
		t.Errorf("tca = %s, want %s", r.tca, tca)
	}
	if r.missKm != 0.842 || r.relSpeed != 7.512 {
		t.Errorf("miss = %v, relSpeed = %v; want 0.842, 7.512", r.missKm, r.relSpeed)
	}
	// epoch = TCA minus the days-since-epoch columns.
	if want := tca.Add(-36 * time.Hour); !r.epochA.Equal(want) { // 1.5 days
		t.Errorf("epochA = %s, want %s", r.epochA, want)
	}
	if want := tca.Add(-12 * time.Hour); !r.epochB.Equal(want) { // 0.5 days
		t.Errorf("epochB = %s, want %s", r.epochB, want)
	}
}

// When the first object has the larger catalogue number, a and b are swapped
// so that a < b, and each object's epoch must travel with its own id.
func TestReadReferenceSwapsPairAndKeepsEpochsWithTheirObjects(t *testing.T) {
	tca := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	path := writeCSV(t, header,
		"500,300,1.0,2.0,2026-11-06 12:00:00.000,1.0,5.0")
	refs, err := readReference(path)
	if err != nil {
		t.Fatal(err)
	}
	r := refs[0]
	if r.a != 300 || r.b != 500 {
		t.Fatalf("ids = %d, %d; want 300, 500", r.a, r.b)
	}
	// Object 300 came from DSE_2 = 2 days, object 500 from DSE_1 = 1 day.
	if want := tca.Add(-48 * time.Hour); !r.epochA.Equal(want) {
		t.Errorf("epoch of object 300: %s, want %s", r.epochA, want)
	}
	if want := tca.Add(-24 * time.Hour); !r.epochB.Equal(want) {
		t.Errorf("epoch of object 500: %s, want %s", r.epochB, want)
	}
}

// Columns are looked up by name, so a different order and an extra column
// must not matter.
func TestReadReferenceIsIndependentOfColumnOrder(t *testing.T) {
	path := writeCSV(t,
		"EXTRA,TCA,NORAD_CAT_ID_2,NORAD_CAT_ID_1,TCA_RELATIVE_SPEED,TCA_RANGE,DSE_2,DSE_1",
		"ignored,2026-11-06 12:00:00.000,200,100,7.5,0.84,0.5,1.5")
	refs, err := readReference(path)
	if err != nil {
		t.Fatal(err)
	}
	r := refs[0]
	if r.a != 100 || r.b != 200 || r.missKm != 0.84 || r.relSpeed != 7.5 {
		t.Errorf("parsed %+v", r)
	}
}

func TestReadReferenceErrors(t *testing.T) {
	if _, err := readReference(filepath.Join(t.TempDir(), "absent.csv")); err == nil {
		t.Error("missing file: no error")
	}
	if _, err := readReference(writeCSV(t, header)); err == nil {
		t.Error("header-only file: no error")
	}
	if _, err := readReference(writeCSV(t, "NORAD_CAT_ID_1,DSE_1,DSE_2,TCA,TCA_RANGE,TCA_RELATIVE_SPEED",
		"100,1,1,2026-11-06 12:00:00.000,1,1")); err == nil {
		t.Error("missing NORAD_CAT_ID_2 column: no error")
	}
	if _, err := readReference(writeCSV(t, header,
		"100,200,1,1,not-a-time,1,1")); err == nil {
		t.Error("unparseable TCA: no error")
	}
}

func TestPercentile(t *testing.T) {
	// The code indexes sorted[int((len-1)*p)]: a floor, not interpolation.
	sorted := []float64{10, 20, 30, 40, 50}
	for _, c := range []struct {
		p    float64
		want float64
	}{{0.5, 30}, {0.95, 40}, {0.99, 40}, {1, 50}, {0, 10}} {
		if got := percentile(sorted, c.p); got != c.want {
			t.Errorf("percentile(%.2f) = %v, want %v", c.p, got, c.want)
		}
	}
	if got := percentile(nil, 0.5); !math.IsNaN(got) {
		t.Errorf("percentile of empty = %v, want NaN", got)
	}
}

func TestSummarizeSortsAndReportsSpread(t *testing.T) {
	got := summarize([]float64{50, 10, 40, 20, 30})
	want := spread{Median: 30, P95: 40, P99: 40, Max: 50}
	if got != want {
		t.Errorf("summarize = %+v, want %+v", got, want)
	}
}

func TestAbsDuration(t *testing.T) {
	if got := absDuration(-3 * time.Second); got != 3*time.Second {
		t.Errorf("absDuration(-3s) = %s", got)
	}
	if got := absDuration(3 * time.Second); got != 3*time.Second {
		t.Errorf("absDuration(3s) = %s", got)
	}
	if got := absDuration(0); got != 0 {
		t.Errorf("absDuration(0) = %s", got)
	}
}
