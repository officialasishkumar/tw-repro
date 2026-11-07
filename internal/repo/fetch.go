// Package repo finds the official copy of an installed RPM.
package repo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// DefaultBaseURL is the Tumbleweed OSS repository; download.opensuse.org
// redirects to a mirror and the http client follows.
const DefaultBaseURL = "https://download.opensuse.org/tumbleweed/repo/oss"

// Fetcher opens official RPMs from the zypp cache or the repository.
type Fetcher struct {
	BaseURL    string
	CacheDir   string // /var/cache/zypp/packages
	HTTP       *http.Client
	Downloaded atomic.Int64
}

// New returns a Fetcher with the defaults.
func New() *Fetcher {
	return &Fetcher{BaseURL: DefaultBaseURL, CacheDir: "/var/cache/zypp/packages", HTTP: &http.Client{Timeout: 30 * time.Minute}}
}

type countingReader struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// Open returns the RPM's bytes and where they came from. A 404 means the
// repository has moved on to another build of the package.
func (f *Fetcher) Open(ctx context.Context, arch, file string) (io.ReadCloser, string, error) {
	if matches, _ := filepath.Glob(filepath.Join(f.CacheDir, "*", arch, file)); len(matches) > 0 {
		r, err := os.Open(matches[0])
		return r, matches[0], err
	}
	url := f.BaseURL + "/" + arch + "/" + file
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, url, err
	}
	req.Header.Set("User-Agent", "tw-repro (+https://github.com/officialasishkumar/tw-repro)")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, url, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, url, ErrGone
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, url, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return countingReader{resp.Body, &f.Downloaded}, url, nil
}

// ErrGone means the exact build is no longer in the repository.
var ErrGone = fmt.Errorf("not in the repository any more (the mirror has a newer build)")
