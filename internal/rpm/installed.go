// Package rpm reads the installed package list and RPM files.
package rpm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/officialasishkumar/tw-repro/internal/rb"
)

// QueryFormat is what Installed asks rpm for.
const QueryFormat = `%{NAME}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\t%{SOURCERPM}\n`

// Installed lists the installed packages through rpm -qa.
func Installed(ctx context.Context) ([]rb.Installed, error) {
	out, err := exec.CommandContext(ctx, "rpm", "-qa", "--qf", QueryFormat).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("rpm -qa: %s", bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("rpm -qa: %w (tw-repro needs rpm; it reads the packages installed on this machine)", err)
	}
	return ParseInstalled(bytes.NewReader(out))
}

// ParseInstalled parses the output of rpm -qa with QueryFormat. Entries
// without a source RPM (gpg-pubkey) are skipped.
func ParseInstalled(r *bytes.Reader) ([]rb.Installed, error) {
	var pkgs []rb.Installed
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 5 {
			continue
		}
		p := rb.Installed{Name: f[0], Version: f[1], Release: f[2], Arch: f[3], SourceRPM: f[4]}
		if p.SourceRPM == "(none)" || p.SourceRPM == "" || p.Arch == "(none)" {
			continue
		}
		pkgs = append(pkgs, p)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].Name != pkgs[j].Name {
			return pkgs[i].Name < pkgs[j].Name
		}
		return pkgs[i].Arch < pkgs[j].Arch
	})
	return pkgs, nil
}

// FileName is the RPM file name of an installed package in the repository.
func FileName(p rb.Installed) string {
	return p.Name + "-" + p.Version + "-" + p.Release + "." + p.Arch + ".rpm"
}
