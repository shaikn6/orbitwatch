package main

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWriteEventsCSV(t *testing.T) {
	events := []viewerEvent{
		{A: 25544, BID: 48274, NameA: "ISS (ZARYA)", NameB: "CSS (TIANHE)", KindA: "active satellite", KindB: "active satellite",
			TCA: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), MissKm: 0.8421, RelSpeedKmS: 7.512, Lat: 12.3, Lon: -45.6, AltKm: 418.2},
		{A: 12345, BID: 67890, NameA: "COSMOS 1408 DEB", NameB: "FENGYUN 1C DEB", KindA: "debris", KindB: "debris",
			TCA: time.Date(2026, 11, 6, 13, 30, 0, 0, time.UTC), MissKm: 0.1903, RelSpeedKmS: 13.04, Lat: -3.1, Lon: 120.7, AltKm: 790.5},
	}

	path := filepath.Join(t.TempDir(), "conjunctions.csv")
	if err := writeEventsCSV(path, events); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("CSV did not parse: %v", err)
	}

	// Header plus one row per event, matching the JSON output's event count.
	jsonData, _ := json.Marshal(summary{Events: events})
	var decoded summary
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatal(err)
	}
	if want := len(decoded.Events) + 1; len(rows) != want {
		t.Fatalf("got %d CSV rows, want %d (header + %d events)", len(rows), want, len(decoded.Events))
	}

	header := []string{"norad_a", "norad_b", "name_a", "name_b", "kind_a", "kind_b", "tca", "miss_km", "rel_speed_km_s", "lat", "lon", "alt_km"}
	if strings.Join(rows[0], ",") != strings.Join(header, ",") {
		t.Fatalf("header = %v, want %v", rows[0], header)
	}
	for i, row := range rows[1:] {
		if len(row) != len(header) {
			t.Fatalf("row %d has %d columns, want %d", i, len(row), len(header))
		}
		if _, err := strconv.Atoi(row[0]); err != nil {
			t.Errorf("row %d norad_a %q not an int: %v", i, row[0], err)
		}
		if _, err := time.Parse(time.RFC3339, row[6]); err != nil {
			t.Errorf("row %d tca %q not RFC3339: %v", i, row[6], err)
		}
		if _, err := strconv.ParseFloat(row[7], 64); err != nil {
			t.Errorf("row %d miss_km %q not a float: %v", i, row[7], err)
		}
	}
}

func TestViewerServerServesIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "a.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(viewerServer("", dir).Handler)
	defer ts.Close()

	get := func(path string) *http.Response {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	resp := get("/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /: content type %q, want text/html", ct)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := get("/data/a.json").Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control on /data/ = %q, want no-cache", got)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", nil)
	post, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /: status %d, want 405", post.StatusCode)
	}
}
