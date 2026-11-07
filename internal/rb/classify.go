package rb

import "fmt"

// Class is the verdict for one installed package.
type Class string

const (
	// Reproduced: the rebuilder built the same source independently and the
	// official RPMs are bit-identical to the result once their GPG
	// signature is removed (verified = 2).
	Reproduced Class = "reproduced"
	// Equivalent: the rebuild is not bit-identical to the official RPMs, but
	// build-compare found no meaningful difference (verified = 1).
	Equivalent Class = "equivalent"
	// VerifyFailed: the rebuild of the official source produced different
	// binaries (verified = 0).
	VerifyFailed Class = "verify-failed"
	// NotCovered: there is no comparison with the official binaries: the
	// source package is not in the comparison, its rebuild failed or was
	// skipped, or the installed binary was not produced by the rebuild.
	NotCovered Class = "not-covered"
)

// Installed is what tw-repro needs to know about an installed package.
type Installed struct {
	Name      string
	Version   string
	Release   string
	Arch      string
	SourceRPM string // e.g. curl-mini-8.23.0-1.1.src.rpm
}

// Verdict is the classification of one installed package.
type Verdict struct {
	Installed
	OBSPackage string `json:"obs_package,omitempty"`
	Class      Class  `json:"class"`
	// Differed is true when the rebuilder's two varied local builds of this
	// source differ from each other (status = unreproducible).
	Differed bool `json:"differed"`
	// Minor is true when build-compare considers that difference harmless.
	Minor bool `json:"minor,omitempty"`
	// RebuiltVersion is the version the rebuilder tested; Stale is true when
	// it is not the installed version, in which case the verdict is about
	// another build of the package.
	RebuiltVersion string `json:"rebuilt_version,omitempty"`
	Stale          bool   `json:"stale,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// Data is everything Classify needs.
type Data struct {
	Entries   map[string]Entry
	Checksums *Checksums
}

// OBSPackage maps a source RPM file name to the OBS package that builds it,
// using checksums.txt first and the package list second.
func (d *Data) OBSPackage(sourceRPM string) (string, bool) {
	nvra, ok := ParseNVRA(sourceRPM)
	if !ok {
		return "", false
	}
	if d.Checksums != nil {
		if obs, ok := d.Checksums.SourceToOBS[nvra.Name]; ok {
			return obs, true
		}
	}
	if _, ok := d.Entries[nvra.Name]; ok {
		return nvra.Name, true
	}
	return "", false
}

// Classify applies the rules in the package comment to one installed package.
func (d *Data) Classify(p Installed) Verdict {
	v := Verdict{Installed: p, Class: NotCovered}
	obs, ok := d.OBSPackage(p.SourceRPM)
	if !ok {
		v.Reason = "source package not in the comparison"
		return v
	}
	v.OBSPackage = obs
	e, ok := d.Entries[obs]
	if !ok {
		v.Reason = "no result for " + obs
		return v
	}
	v.Differed = e.Status == "unreproducible"
	v.Minor = v.Differed && e.BuildCompare == "reproducible"
	v.RebuiltVersion = e.UpstreamVersion()
	if d.Checksums != nil {
		if ver, ok := d.Checksums.Versions[obs]; ok {
			v.RebuiltVersion = ver
		}
	}
	if v.RebuiltVersion != "" && v.RebuiltVersion != p.Version {
		v.Stale = true
	}
	if d.Checksums != nil {
		if bins, ok := d.Checksums.Binaries[obs]; ok && len(bins) > 0 && !bins[p.Name] {
			v.Reason = fmt.Sprintf("the rebuild of %s did not produce %s", obs, p.Name)
			return v
		}
	}
	switch {
	case e.Verified == nil:
		v.Reason = "rebuild not compared with the official binaries (" + e.Status + ")"
	case *e.Verified >= 2:
		v.Class = Reproduced
	case *e.Verified == 1:
		v.Class = Equivalent
	default:
		v.Class = VerifyFailed
	}
	return v
}

// Summary counts verdicts.
type Summary struct {
	Total        int `json:"total"`
	Reproduced   int `json:"reproduced"`
	Equivalent   int `json:"equivalent"`
	VerifyFailed int `json:"verify_failed"`
	NotCovered   int `json:"not_covered"`
	Differed     int `json:"differed"`
	Stale        int `json:"stale"`
}

// Summarize counts the classes in vs.
func Summarize(vs []Verdict) Summary {
	var s Summary
	for _, v := range vs {
		s.Total++
		switch v.Class {
		case Reproduced:
			s.Reproduced++
		case Equivalent:
			s.Equivalent++
		case VerifyFailed:
			s.VerifyFailed++
		default:
			s.NotCovered++
		}
		if v.Differed {
			s.Differed++
		}
		if v.Stale {
			s.Stale++
		}
	}
	return s
}

// Percent of reproduced packages among those the rebuild covers.
func (s Summary) Percent() float64 {
	covered := s.Total - s.NotCovered
	if covered == 0 {
		return 0
	}
	return 100 * float64(s.Reproduced) / float64(covered)
}

// String is the one-line summary.
func (s Summary) String() string {
	return fmt.Sprintf("%s packages: %.1f%% of the %s covered by the rebuild are reproduced bit-for-bit, %d equivalent, %d failed verification, %d not covered; %d differ between the rebuilder's two varied builds",
		commas(s.Total), s.Percent(), commas(s.Total-s.NotCovered), s.Equivalent, s.VerifyFailed, s.NotCovered, s.Differed)
}

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
