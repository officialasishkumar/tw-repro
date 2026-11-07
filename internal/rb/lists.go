// Package rb reads the openSUSE Factory rebuild comparison that Bernhard
// M. Wiedemann publishes at https://rb.zq1.de/compare.factory/ and turns it
// into a verdict per source package.
//
// The files and what they mean (from the scripts in
// https://github.com/bmwiedemann/reproducibleopensuse, see README.md):
//
//   - reproducible.json: one record per source package in openSUSE:Factory
//     that the rebuilder tried. "status" is the result of building the
//     package twice locally with a different date, hostname and CPU count
//     ("reproducible" when the two builds are bit-identical, "unreproducible"
//     when they differ, or FTBFS/waitdep/notforus/nobinaries when there was
//     nothing to compare). "verified" is the result of rebuilding the exact
//     source of the official binary and comparing with the official RPMs
//     after removing their GPG signature: 2 = bit-identical, 1 = not
//     bit-identical but equivalent for build-compare, 0 = different, absent
//     = not done.
//   - checksums.txt: md5 and sha256 of every RPM the two local builds
//     produced, as "<hash>  <obs package>/RPMS/<file>" (second build) and
//     "<obs package>/RPMS.2/<file>" (first build). This is also the only
//     published mapping from source RPM name to OBS package name.
//   - allnotes.txt: the rebuilder's per-package notes (".rb.notes"), which
//     usually name the cause and the submit request or upstream fix.
package rb

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Entry is one record of reproducible.json.
type Entry struct {
	Package  string `json:"package"`
	Version  string `json:"version"` // "<upstream version>-<OBS revision>"
	Status   string `json:"status"`  // reproducible, unreproducible, FTBFS, waitdep, notforus, nobinaries
	Verified *int   `json:"verified"`
	// BuildCompare is set when Status is "unreproducible": "reproducible"
	// means build-compare considered the differences between the two local
	// builds harmless.
	BuildCompare string `json:"build_compare_status"`
	BuildDate    int64  `json:"build_date"`
	SigDate      string `json:"opensusesigdate"`
}

// UpstreamVersion returns the version without the trailing OBS revision.
func (e Entry) UpstreamVersion() string {
	if i := strings.LastIndexByte(e.Version, '-'); i > 0 {
		return e.Version[:i]
	}
	return e.Version
}

// ParseJSON parses reproducible.json into a map keyed by OBS package name.
func ParseJSON(r io.Reader) (map[string]Entry, error) {
	var list []Entry
	if err := json.NewDecoder(r).Decode(&list); err != nil {
		return nil, fmt.Errorf("reproducible.json: %w", err)
	}
	m := make(map[string]Entry, len(list))
	for _, e := range list {
		if e.Package != "" {
			m[e.Package] = e
		}
	}
	return m, nil
}

// Checksum is one rebuilt RPM from checksums.txt.
type Checksum struct {
	OBSPackage string // first path component
	Dir        string // RPMS (second build) or RPMS.2 (first build)
	File       string // e.g. bash-5.3.20-1.1.x86_64.rpm
	MD5        string
	SHA256     string
}

// Checksums is the parsed checksums.txt.
type Checksums struct {
	// ByFile maps an RPM file name to its entries (one per build directory).
	ByFile map[string][]Checksum
	// SourceToOBS maps a source RPM name (e.g. "curl-mini") to the OBS
	// package that produced it (e.g. "curl").
	SourceToOBS map[string]string
	// Binaries maps an OBS package to the set of binary RPM names it produced.
	Binaries map[string]map[string]bool
	// Versions maps an OBS package to the version of its rebuilt source RPM.
	Versions map[string]string
}

// ParseChecksums parses checksums.txt. Lines are "<md5|sha256>  <path>".
func ParseChecksums(r io.Reader) (*Checksums, error) {
	c := &Checksums{
		ByFile:      map[string][]Checksum{},
		SourceToOBS: map[string]string{},
		Binaries:    map[string]map[string]bool{},
		Versions:    map[string]string{},
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
		hash, path, ok := strings.Cut(sc.Text(), "  ")
		if !ok {
			continue
		}
		parts := strings.Split(path, "/")
		if len(parts) != 3 || !strings.HasSuffix(parts[2], ".rpm") {
			continue
		}
		obs, dir, file := parts[0], parts[1], parts[2]
		nvra, ok := ParseNVRA(file)
		if !ok {
			continue
		}
		if nvra.Arch == "src" || nvra.Arch == "nosrc" {
			c.SourceToOBS[nvra.Name] = obs
			c.Versions[obs] = nvra.Version
			continue
		}
		set := c.Binaries[obs]
		if set == nil {
			set = map[string]bool{}
			c.Binaries[obs] = set
		}
		set[nvra.Name] = true
		// md5 and sha256 for the same file are on separate lines; merge them.
		list := c.ByFile[file]
		i := -1
		for j := range list {
			if list[j].Dir == dir {
				i = j
			}
		}
		if i < 0 {
			list = append(list, Checksum{OBSPackage: obs, Dir: dir, File: file})
			i = len(list) - 1
		}
		switch len(hash) {
		case 32:
			list[i].MD5 = hash
		case 64:
			list[i].SHA256 = hash
		}
		c.ByFile[file] = list
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("checksums.txt line %d: %w", n, err)
	}
	if n == 0 {
		return nil, fmt.Errorf("checksums.txt is empty")
	}
	return c, nil
}

// NVRA is a parsed RPM file name.
type NVRA struct {
	Name, Version, Release, Arch string
}

// ParseNVRA splits "name-version-release.arch.rpm".
func ParseNVRA(file string) (NVRA, bool) {
	s := strings.TrimSuffix(file, ".rpm")
	if s == file {
		return NVRA{}, false
	}
	dot := strings.LastIndexByte(s, '.')
	if dot < 0 {
		return NVRA{}, false
	}
	arch := s[dot+1:]
	s = s[:dot]
	rel := strings.LastIndexByte(s, '-')
	if rel < 1 {
		return NVRA{}, false
	}
	ver := strings.LastIndexByte(s[:rel], '-')
	if ver < 1 {
		return NVRA{}, false
	}
	return NVRA{Name: s[:ver], Version: s[ver+1 : rel], Release: s[rel+1:], Arch: arch}, true
}

// ParseNotes parses allnotes.txt, which is the output of "head" over every
// package's .rb.notes: blocks start with "==> ./<package>/.rb.notes <==".
func ParseNotes(r io.Reader) map[string]string {
	notes := map[string]string{}
	var cur string
	var buf strings.Builder
	flush := func() {
		if cur != "" {
			notes[cur] = strings.TrimSpace(buf.String())
		}
		buf.Reset()
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "==> ./") && strings.HasSuffix(line, "/.rb.notes <==") {
			flush()
			cur = strings.TrimSuffix(strings.TrimPrefix(line, "==> ./"), "/.rb.notes <==")
			continue
		}
		if cur != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	flush()
	return notes
}
