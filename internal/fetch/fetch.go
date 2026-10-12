// Package fetch keeps a local copy of published element sets up to date.
//
// It is built so that a slow, broken or missing source never leaves the
// project without data: a download only replaces the cached file after it
// has been read completely and parsed as element sets, and a failed
// download leaves the previous copy in place. Files newer than the
// provider's update interval are not requested again.
package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/shaikn6/orbitwatch/internal/catalog"
)

// Source is one downloadable set of element sets.
type Source struct {
	Name string // file name without extension
	URL  string
}

// CelesTrak lists the public sets that together cover active satellites,
// debris and rocket bodies.
var CelesTrak = []Source{
	{"gp_active", "https://celestrak.org/NORAD/elements/gp.php?GROUP=active&FORMAT=json"},
	{"gp_name_deb", "https://celestrak.org/NORAD/elements/gp.php?NAME=DEB&FORMAT=json"},
	{"gp_name_rb", "https://celestrak.org/NORAD/elements/gp.php?NAME=R/B&FORMAT=json"},
}

// Result says what happened to one source.
type Result struct {
	Source  Source
	Objects int
	Fresh   bool  // downloaded now; false means the cached copy was kept
	Err     error // why the download failed, if it did
}

// Options configures Update.
type Options struct {
	Dir       string
	MinAge    time.Duration // do not re-download files younger than this; default 2 h
	MaxBytes  int64         // refuse larger responses; default 64 MiB
	UserAgent string
	Client    *http.Client
}

// Update refreshes every source and reports on each. It returns an error
// only when a source has neither a fresh download nor a usable cached copy.
func Update(ctx context.Context, sources []Source, opts Options) ([]Result, error) {
	if opts.MinAge <= 0 {
		opts.MinAge = 2 * time.Hour
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 64 << 20
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "orbitwatch/0.1"
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	results := make([]Result, len(sources))
	var unusable error
	for i, source := range sources {
		path := filepath.Join(opts.Dir, source.Name+".json")
		results[i] = Result{Source: source}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < opts.MinAge {
			results[i].Objects = countCached(path)
			continue
		}
		count, err := download(ctx, source, path, opts)
		if err == nil {
			results[i].Objects, results[i].Fresh = count, true
			continue
		}
		results[i].Err = err
		results[i].Objects = countCached(path)
		if results[i].Objects == 0 && unusable == nil {
			unusable = fmt.Errorf("%s: no data: %w", source.Name, err)
		}
	}
	return results, unusable
}

func countCached(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	good, _, err := catalog.Parse(file)
	if err != nil {
		return 0
	}
	return len(good)
}

func download(ctx context.Context, source Source, path string, opts Options) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	resp, err := opts.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("server answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBytes+1))
	if err != nil {
		return 0, err
	}
	if int64(len(body)) > opts.MaxBytes {
		return 0, fmt.Errorf("response is larger than %d bytes", opts.MaxBytes)
	}
	// An error page or an empty answer must never replace good data.
	good, _, err := catalog.Parse(bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if len(good) == 0 {
		return 0, fmt.Errorf("response holds no usable element sets")
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return len(good), nil
}
