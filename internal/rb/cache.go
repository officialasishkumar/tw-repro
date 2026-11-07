package rb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL is the current comparison; dated copies live next to it.
const DefaultBaseURL = "https://rb.zq1.de/compare.factory"

// Client downloads the comparison files and keeps them in a cache directory.
type Client struct {
	BaseURL string
	Dir     string
	MaxAge  time.Duration
	HTTP    *http.Client
	Log     io.Writer // progress messages, may be nil
}

// Meta records when a cached file was fetched and what the server said.
type Meta struct {
	FetchedAt    time.Time `json:"fetched_at"`
	LastModified string    `json:"last_modified,omitempty"`
	Size         int64     `json:"size"`
}

// Age is how old the cached copy is.
func (m Meta) Age() time.Duration { return time.Since(m.FetchedAt).Round(time.Minute) }

// ComparisonDate is the server's Last-Modified date, YYYY-MM-DD, or "".
func (m Meta) ComparisonDate() string {
	t, err := http.ParseTime(m.LastModified)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// NewClient returns a client using $XDG_CACHE_HOME/tw-repro.
func NewClient() *Client {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return &Client{
		BaseURL: DefaultBaseURL,
		Dir:     filepath.Join(dir, "tw-repro"),
		MaxAge:  24 * time.Hour,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

func (c *Client) logf(format string, a ...any) {
	if c.Log != nil {
		fmt.Fprintf(c.Log, format+"\n", a...)
	}
}

// File returns the path of a cached copy of name, downloading it when
// missing, older than MaxAge, or when force is set. A stale copy is used if
// the download fails.
func (c *Client) File(ctx context.Context, name string, force bool) (string, Meta, error) {
	path := filepath.Join(c.Dir, name)
	meta, haveMeta := c.readMeta(name)
	if _, err := os.Stat(path); err != nil {
		haveMeta = false
	}
	if haveMeta && !force && time.Since(meta.FetchedAt) < c.MaxAge {
		return path, meta, nil
	}
	newMeta, err := c.download(ctx, name, path, meta, haveMeta)
	if err != nil {
		if haveMeta {
			c.logf("tw-repro: using the cached %s from %s ago: %v", name, meta.Age(), err)
			return path, meta, nil
		}
		return "", Meta{}, err
	}
	return path, newMeta, nil
}

func (c *Client) readMeta(name string) (Meta, bool) {
	b, err := os.ReadFile(filepath.Join(c.Dir, name+".meta"))
	if err != nil {
		return Meta{}, false
	}
	var m Meta
	if json.Unmarshal(b, &m) != nil || m.FetchedAt.IsZero() {
		return Meta{}, false
	}
	return m, true
}

func (c *Client) download(ctx context.Context, name, path string, old Meta, conditional bool) (Meta, error) {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return Meta{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/"+name, nil)
	if err != nil {
		return Meta{}, err
	}
	req.Header.Set("User-Agent", "tw-repro (+https://github.com/officialasishkumar/tw-repro)")
	if conditional && old.LastModified != "" {
		req.Header.Set("If-Modified-Since", old.LastModified)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Meta{}, err
	}
	defer resp.Body.Close()
	meta := Meta{FetchedAt: time.Now(), LastModified: resp.Header.Get("Last-Modified")}
	switch resp.StatusCode {
	case http.StatusNotModified:
		meta.LastModified = old.LastModified
		meta.Size = old.Size
		c.logf("tw-repro: %s unchanged on the server", name)
		return meta, c.writeMeta(name, meta)
	case http.StatusOK:
	default:
		return Meta{}, fmt.Errorf("GET %s: %s", req.URL, resp.Status)
	}
	size := ""
	if resp.ContentLength > 0 {
		size = fmt.Sprintf(" (%s)", HumanBytes(resp.ContentLength))
	}
	c.logf("tw-repro: downloading %s%s", req.URL, size)
	tmp, err := os.CreateTemp(c.Dir, name+".*.part")
	if err != nil {
		return Meta{}, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Meta{}, fmt.Errorf("downloading %s: %w", name, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return Meta{}, err
	}
	meta.Size = n
	return meta, c.writeMeta(name, meta)
}

func (c *Client) writeMeta(name string, m Meta) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(filepath.Join(c.Dir, name+".meta"), b, 0o644)
}

// Load reads reproducible.json and checksums.txt from the cache.
func (c *Client) Load(ctx context.Context, force bool) (*Data, Meta, error) {
	jp, meta, err := c.File(ctx, "reproducible.json", force)
	if err != nil {
		return nil, Meta{}, err
	}
	f, err := os.Open(jp)
	if err != nil {
		return nil, Meta{}, err
	}
	entries, err := ParseJSON(bufio.NewReader(f))
	f.Close()
	if err != nil {
		return nil, Meta{}, err
	}
	cp, _, err := c.File(ctx, "checksums.txt", force)
	if err != nil {
		return nil, Meta{}, err
	}
	f, err = os.Open(cp)
	if err != nil {
		return nil, Meta{}, err
	}
	sums, err := ParseChecksums(bufio.NewReaderSize(f, 1<<20))
	f.Close()
	if err != nil {
		return nil, Meta{}, err
	}
	return &Data{Entries: entries, Checksums: sums}, meta, nil
}

// Notes returns the rebuilder's per-package notes.
func (c *Client) Notes(ctx context.Context, force bool) (map[string]string, error) {
	p, _, err := c.File(ctx, "allnotes.txt", force)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseNotes(f), nil
}

// Update refreshes every file tw-repro uses.
func (c *Client) Update(ctx context.Context) error {
	var errs []error
	for _, name := range []string{"reproducible.json", "checksums.txt", "allnotes.txt"} {
		if _, _, err := c.File(ctx, name, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CompareOut fetches the first lines of the build-compare output the site
// publishes for a package whose two local builds differed. ok is false when
// there is none.
func (c *Client) CompareOut(ctx context.Context, pkg string, lines int) (url string, head []string, ok bool, err error) {
	url = c.BaseURL + "/diffs/" + pkg + "-compare.out"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return url, nil, false, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return url, nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return url, nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return url, nil, false, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() && len(head) < lines {
		head = append(head, strings.TrimRight(sc.Text(), " "))
	}
	return url, head, true, nil
}

// HumanBytes formats a byte count.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
