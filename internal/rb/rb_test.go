package rb

import (
	"strings"
	"testing"
)

const checksumsSample = `6a656f7e05b58abe2843b6fdcb0c0f74  bash/RPMS.2/bash-5.3.20-1.1.src.rpm
a82cd2990e1d9d3a881a3a4992cbd9fe  bash/RPMS.2/bash-5.3.20-1.1.x86_64.rpm
2548f63f3c7237b42f5a7eaa6369d0006dfe20e613432f686fba59683d7cf85e  bash/RPMS.2/bash-5.3.20-1.1.x86_64.rpm
a82cd2990e1d9d3a881a3a4992cbd9fe  bash/RPMS/bash-5.3.20-1.1.x86_64.rpm
2c3dab98784f95c81b967f0aa7d9dc69  bash/RPMS/bash-sh-5.3.20-1.1.noarch.rpm
96c489018d9e720e3a3dc430f0296576  curl/RPMS/curl-mini-8.23.0-1.1.src.rpm
28142b119579405515bc008ab1335144  curl/RPMS/libcurl-mini4-8.23.0-1.1.x86_64.rpm
garbage line
`

func TestParseChecksums(t *testing.T) {
	c, err := ParseChecksums(strings.NewReader(checksumsSample))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.SourceToOBS["curl-mini"]; got != "curl" {
		t.Errorf("curl-mini -> %q, want curl", got)
	}
	if got := c.Versions["bash"]; got != "5.3.20" {
		t.Errorf("bash version %q", got)
	}
	e := c.ByFile["bash-5.3.20-1.1.x86_64.rpm"]
	if len(e) != 2 {
		t.Fatalf("want entries for RPMS and RPMS.2, got %+v", e)
	}
	if e[0].Dir != "RPMS.2" || e[0].MD5 != "a82cd2990e1d9d3a881a3a4992cbd9fe" || !strings.HasPrefix(e[0].SHA256, "2548f6") {
		t.Errorf("md5 and sha256 not merged: %+v", e[0])
	}
	if !c.Binaries["curl"]["libcurl-mini4"] || c.Binaries["curl"]["curl"] {
		t.Errorf("binaries of curl: %v", c.Binaries["curl"])
	}
}

func TestParseNVRA(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want NVRA
		ok   bool
	}{
		{"bash-5.3.20-1.1.x86_64.rpm", NVRA{"bash", "5.3.20", "1.1", "x86_64"}, true},
		{"openal-soft-devel-1.25.2~0-1.1.x86_64.rpm", NVRA{"openal-soft-devel", "1.25.2~0", "1.1", "x86_64"}, true},
		{"curl-mini-8.23.0-1.1.src.rpm", NVRA{"curl-mini", "8.23.0", "1.1", "src"}, true},
		{"nonsense", NVRA{}, false},
		{"a-b.rpm", NVRA{}, false},
	} {
		got, ok := ParseNVRA(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got %+v %v, want %+v %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseJSONAndNotes(t *testing.T) {
	src := `[{"package":"bash","version":"5.3.20-209","status":"reproducible","verified":2},
	{"package":"emacs","version":"31.1-227","status":"unreproducible","build_compare_status":"unreproducible","verified":0},
	{"package":"ShellCheck","version":"0.11.0-31","status":"waitdep","verified":null}]`
	m, err := ParseJSON(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 || *m["bash"].Verified != 2 || m["ShellCheck"].Verified != nil || m["bash"].UpstreamVersion() != "5.3.20" {
		t.Errorf("parsed %+v", m)
	}
	notes := ParseNotes(strings.NewReader("==> ./boost/.rb.notes <==\n=> SR 1374641 race\n\n==> ./emacs/.rb.notes <==\nsome CPU-dependence remaining\n"))
	if notes["boost"] != "=> SR 1374641 race" || notes["emacs"] != "some CPU-dependence remaining" {
		t.Errorf("notes %q", notes)
	}
}

func TestClassify(t *testing.T) {
	two, one, zero := 2, 1, 0
	sums, _ := ParseChecksums(strings.NewReader(checksumsSample))
	d := &Data{Checksums: sums, Entries: map[string]Entry{
		"bash":  {Package: "bash", Version: "5.3.20-209", Status: "reproducible", Verified: &two},
		"curl":  {Package: "curl", Version: "8.23.0-229", Status: "reproducible", Verified: &two},
		"rsync": {Package: "rsync", Version: "3.5.1-101", Status: "unreproducible", BuildCompare: "reproducible", Verified: &two},
		"emacs": {Package: "emacs", Version: "31.1-227", Status: "unreproducible", BuildCompare: "unreproducible", Verified: &zero},
		"zlib":  {Package: "zlib", Version: "1.3.1-5", Status: "reproducible", Verified: &one},
		"foo":   {Package: "foo", Version: "1-1", Status: "FTBFS"},
	}}
	for _, tc := range []struct {
		p           Installed
		class       Class
		differed    bool
		stale       bool
		obs, reason string
	}{
		{Installed{"bash", "5.3.20", "10.2", "x86_64", "bash-5.3.20-10.2.src.rpm"}, Reproduced, false, false, "bash", ""},
		{Installed{"bash", "5.4", "1.1", "x86_64", "bash-5.4-1.1.src.rpm"}, Reproduced, false, true, "bash", ""},
		{Installed{"libcurl-mini4", "8.23.0", "1.1", "x86_64", "curl-mini-8.23.0-1.1.src.rpm"}, Reproduced, false, false, "curl", ""},
		{Installed{"libcurl4", "8.23.0", "1.1", "x86_64", "curl-8.23.0-1.1.src.rpm"}, NotCovered, false, false, "curl", "the rebuild of curl did not produce libcurl4"},
		{Installed{"rsync", "3.5.1", "1.1", "x86_64", "rsync-3.5.1-1.1.src.rpm"}, Reproduced, true, false, "rsync", ""},
		{Installed{"emacs", "31.1", "1.1", "x86_64", "emacs-31.1-1.1.src.rpm"}, VerifyFailed, true, false, "emacs", ""},
		{Installed{"libz1", "1.3.1", "1.1", "x86_64", "zlib-1.3.1-1.1.src.rpm"}, Equivalent, false, false, "zlib", ""},
		{Installed{"foo", "1", "1.1", "x86_64", "foo-1-1.1.src.rpm"}, NotCovered, false, false, "foo", "rebuild not compared with the official binaries (FTBFS)"},
		{Installed{"bar", "1", "1.1", "x86_64", "bar-1-1.1.src.rpm"}, NotCovered, false, false, "", "source package not in the comparison"},
	} {
		v := d.Classify(tc.p)
		if v.Class != tc.class || v.Differed != tc.differed || v.Stale != tc.stale || v.OBSPackage != tc.obs || v.Reason != tc.reason {
			t.Errorf("%s: got %+v", tc.p.Name, v)
		}
	}
	s := Summarize([]Verdict{{Class: Reproduced}, {Class: Reproduced, Differed: true}, {Class: VerifyFailed}, {Class: NotCovered}})
	if s.Total != 4 || s.Reproduced != 2 || s.Differed != 1 || s.NotCovered != 1 || s.Percent() != 100*2.0/3 {
		t.Errorf("summary %+v", s)
	}
	if !strings.HasPrefix(s.String(), "4 packages: 66.7% of the 3 covered") {
		t.Errorf("summary line %q", s.String())
	}
}
