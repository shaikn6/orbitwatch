package sgp4

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tleEpoch reads the epoch of a two-line element set as a time.
func tleEpoch(line1 string) time.Time {
	year, _ := strconv.Atoi(strings.TrimSpace(line1[18:20]))
	if year < 57 {
		year += 2000
	} else {
		year += 1900
	}
	days, _ := strconv.ParseFloat(strings.TrimSpace(line1[20:32]), 64)
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	return start.Add(time.Duration((days - 1) * 86400 * float64(time.Second)))
}

func fromTLE(t *testing.T, line1, line2 string) (*Orbit, error) {
	t.Helper()
	raw := ParseTLE(line1, line2, GravityWGS72)
	return FromElements(Elements{
		NoradID: int(raw.satnum), Epoch: tleEpoch(line1),
		MeanMotion: raw.no, Eccentricity: raw.ecco, Inclination: raw.inclo,
		RAAN: raw.nodeo, ArgPerigee: raw.argpo, MeanAnomaly: raw.mo, BStar: raw.bstar,
	}, 'a')
}

// The official verification set: 33 objects chosen to exercise every branch
// of the model, with the positions the reference implementation gives.
func TestMatchesTheOfficialVerificationSet(t *testing.T) {
	orbits := map[int]*Orbit{}
	file, err := os.Open("testdata/SGP4-VER.TLE")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var line1 string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "1 "):
			line1 = line
		case strings.HasPrefix(line, "2 ") && line1 != "":
			orbit, err := fromTLE(t, line1, line)
			if err != nil {
				t.Logf("skipping an object the model rejects at start: %v", err)
				continue
			}
			orbits[orbit.NoradID] = orbit
		}
	}

	expected, err := os.Open("testdata/expected.out")
	if err != nil {
		t.Fatal(err)
	}
	defer expected.Close()
	var current *Orbit
	var worst float64
	var worstAt string
	checked, objects := 0, map[int]bool{}
	scanner = bufio.NewScanner(expected)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == "xx" {
			id, _ := strconv.Atoi(fields[0])
			current = orbits[id]
			continue
		}
		if current == nil || len(fields) < 7 {
			continue
		}
		var want [7]float64
		for i := range want {
			want[i], _ = strconv.ParseFloat(fields[i], 64)
		}
		pos, vel, err := current.At(want[0])
		if err != nil {
			t.Errorf("object %d at %.1f min: reference has a position, we got %v", current.NoradID, want[0], err)
			continue
		}
		miss := math.Sqrt(sq(pos[0]-want[1]) + sq(pos[1]-want[2]) + sq(pos[2]-want[3]))
		speed := math.Sqrt(sq(vel[0]-want[4]) + sq(vel[1]-want[5]) + sq(vel[2]-want[6]))
		if miss > worst {
			worst, worstAt = miss, strconv.Itoa(current.NoradID)+" at "+fields[0]+" min"
		}
		if miss > 1e-6 || speed > 1e-8 {
			t.Errorf("object %d at %.1f min: off by %.3g km and %.3g km/s", current.NoradID, want[0], miss, speed)
		}
		checked++
		objects[current.NoradID] = true
	}
	t.Logf("%d positions of %d objects checked; worst disagreement %.2e km (object %s)", checked, len(objects), worst, worstAt)
	if len(objects) < 30 || checked < 500 {
		t.Fatalf("only %d objects and %d positions were checked; the test data did not load", len(objects), checked)
	}
}

func sq(x float64) float64 { return x * x }

func TestRejectsElementsThatCannotBeAnOrbit(t *testing.T) {
	good := Elements{NoradID: 25544, Epoch: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), MeanMotion: 15.5, Eccentricity: 0.0005, Inclination: 51.6}
	if _, err := FromElements(good, 'i'); err != nil {
		t.Fatalf("valid elements rejected: %v", err)
	}
	for name, change := range map[string]func(*Elements){
		"zero mean motion":      func(e *Elements) { e.MeanMotion = 0 },
		"infinite mean motion":  func(e *Elements) { e.MeanMotion = math.Inf(1) },
		"NaN mean motion":       func(e *Elements) { e.MeanMotion = math.NaN() },
		"orbit below ground":    func(e *Elements) { e.MeanMotion = 25 },
		"infinite drag term":    func(e *Elements) { e.BStar = math.Inf(-1) },
		"eccentricity of one":   func(e *Elements) { e.Eccentricity = 1 },
		"negative eccentricity": func(e *Elements) { e.Eccentricity = -0.1 },
		"missing epoch":         func(e *Elements) { e.Epoch = time.Time{} },
		"NaN inclination":       func(e *Elements) { e.Inclination = math.NaN() },
	} {
		bad := good
		change(&bad)
		if _, err := FromElements(bad, 'i'); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSubSecondEpochIsNotRounded(t *testing.T) {
	e := Elements{NoradID: 1, Epoch: time.Date(2026, 10, 12, 6, 30, 15, 0, time.UTC), MeanMotion: 15.5, Eccentricity: 0.001, Inclination: 53}
	base, _ := FromElements(e, 'i')
	e.Epoch = e.Epoch.Add(900 * time.Millisecond)
	later, _ := FromElements(e, 'i')
	at := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)
	p1, _, _ := base.AtTime(at)
	p2, _, _ := later.AtTime(at)
	moved := math.Sqrt(sq(p1[0]-p2[0]) + sq(p1[1]-p2[1]) + sq(p1[2]-p2[2]))
	if moved < 5 || moved > 9 {
		t.Fatalf("shifting the epoch by 0.9 s moved the object %.2f km; a low orbit should move about 7 km", moved)
	}
}

func BenchmarkAt(b *testing.B) {
	o, err := FromElements(Elements{NoradID: 25544, Epoch: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), MeanMotion: 15.5, Eccentricity: 0.0005, Inclination: 51.6, RAAN: 10, ArgPerigee: 20, MeanAnomaly: 30, BStar: 0.0002}, 'i')
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := range b.N {
		if _, _, err := o.At(float64(i%10000) * 0.5); err != nil {
			b.Fatal(err)
		}
	}
}

func TestGeodetic(t *testing.T) {
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	g := GMST(at)
	// A point 500 km above the equator on the Greenwich meridian.
	r := 6378.137 + 500
	lat, lon, alt := Geodetic([3]float64{r * math.Cos(g), r * math.Sin(g), 0}, at)
	if math.Abs(lat) > 1e-6 || math.Abs(lon) > 1e-6 || math.Abs(alt-500) > 1e-6 {
		t.Fatalf("equator point: lat %.6f lon %.6f alt %.6f", lat, lon, alt)
	}
	// Straight above the north pole; the polar radius is 6356.752 km.
	lat, _, alt = Geodetic([3]float64{0, 0, 6356.7523142 + 800}, at)
	if math.Abs(lat-90) > 1e-6 || math.Abs(alt-800) > 1e-3 {
		t.Fatalf("pole point: lat %.6f alt %.6f", lat, alt)
	}
	// 90 degrees east of Greenwich.
	_, lon, _ = Geodetic([3]float64{r * math.Cos(g+math.Pi/2), r * math.Sin(g+math.Pi/2), 0}, at)
	if math.Abs(lon-90) > 1e-6 {
		t.Fatalf("lon = %.6f, want 90", lon)
	}
	// The Earth turns once per sidereal day.
	turned := math.Mod(GMST(at.Add(86164090530833*time.Microsecond/1000))-g+4*math.Pi, 2*math.Pi)
	if turned > 1e-4 && turned < 2*math.Pi-1e-4 {
		t.Fatalf("after one sidereal day GMST changed by %.6f rad", turned)
	}
}

func TestAtRefusesAbsurdTimeSpans(t *testing.T) {
	o, err := FromElements(Elements{NoradID: 1, Epoch: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), MeanMotion: 2.0, Eccentricity: 0.7, Inclination: 63.4}, 'i')
	if err != nil {
		t.Fatal(err)
	}
	for _, minutes := range []float64{MaxMinutes * 2, -MaxMinutes * 2, math.Inf(1), math.NaN()} {
		if _, _, err := o.At(minutes); err == nil {
			t.Errorf("At(%v) was accepted", minutes)
		}
	}
	if _, _, err := o.At(1440); err != nil {
		t.Errorf("At(one day) = %v", err)
	}
}
