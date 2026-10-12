package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const one = `[{"OBJECT_NAME":"ISS (ZARYA)","EPOCH":"2026-10-12T08:00:00","MEAN_MOTION":15.5,"ECCENTRICITY":0.0004,"INCLINATION":51.6,"NORAD_CAT_ID":25544}]`
const two = `[{"OBJECT_NAME":"A","EPOCH":"2026-10-12T08:00:00","MEAN_MOTION":15.5,"ECCENTRICITY":0.0004,"INCLINATION":51.6,"NORAD_CAT_ID":1},
{"OBJECT_NAME":"B","EPOCH":"2026-10-12T08:00:00","MEAN_MOTION":14.2,"ECCENTRICITY":0.001,"INCLINATION":98,"NORAD_CAT_ID":2}]`

func TestUpdate(t *testing.T) {
	var body atomic.Value
	var status atomic.Int64
	var hits atomic.Int64
	body.Store(one)
	status.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(int(status.Load()))
		w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "set.json")
	sources := []Source{{"set", srv.URL}}
	opts := Options{Dir: dir, UserAgent: "test-agent", MaxBytes: 4096}
	old := func() { // make the cached copy look stale
		past := time.Now().Add(-3 * time.Hour)
		os.Chtimes(path, past, past)
	}

	results, err := Update(context.Background(), sources, opts)
	if err != nil || !results[0].Fresh || results[0].Objects != 1 {
		t.Fatalf("first download: %+v, %v", results, err)
	}

	// A copy younger than the update interval is not requested again.
	results, _ = Update(context.Background(), sources, opts)
	if results[0].Fresh || results[0].Objects != 1 || hits.Load() != 1 {
		t.Fatalf("fresh cache was re-downloaded: %+v after %d requests", results, hits.Load())
	}

	// Each kind of bad answer must leave the good copy in place.
	for name, breakIt := range map[string]func(){
		"server error": func() { status.Store(503) },
		"error page":   func() { body.Store("<html>Service unavailable</html>") },
		"empty list":   func() { body.Store("[]") },
		"oversized":    func() { body.Store("[" + strings.Repeat(" ", 5000) + "]") },
		"truncated":    func() { body.Store(one[:60]) },
	} {
		status.Store(200)
		body.Store(one)
		breakIt()
		old()
		results, err = Update(context.Background(), sources, opts)
		if err != nil || results[0].Err == nil || results[0].Fresh || results[0].Objects != 1 {
			t.Errorf("%s: results %+v, err %v; want the failure reported and the cached copy kept", name, results, err)
		}
		if data, _ := os.ReadFile(path); string(data) != one {
			t.Errorf("%s: the cached copy was changed", name)
		}
	}

	// A good answer replaces the copy.
	status.Store(200)
	body.Store(two)
	old()
	results, err = Update(context.Background(), sources, opts)
	if err != nil || !results[0].Fresh || results[0].Objects != 2 {
		t.Fatalf("refresh: %+v, %v", results, err)
	}
}

func TestUpdateFailsOnlyWhenThereIsNoDataAtAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	_, err := Update(context.Background(), []Source{{"set", srv.URL}}, Options{Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no data") {
		t.Fatalf("err = %v, want a no-data error", err)
	}
}
