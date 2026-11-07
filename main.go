// tw-repro answers "how much of my openSUSE Tumbleweed was independently
// rebuilt bit-for-bit?" from the rebuild comparison at rb.zq1.de.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/officialasishkumar/tw-repro/internal/rb"
	"github.com/officialasishkumar/tw-repro/internal/repo"
	"github.com/officialasishkumar/tw-repro/internal/rpm"
)

var version = "dev"

const usage = `tw-repro: is my Tumbleweed really reproducible?

Usage:
  tw-repro status [--json] [--all]        classify every installed package against the rebuild comparison
  tw-repro verify [--sample N | PKG...]   hash the official RPM and compare with the rebuilder's checksums
  tw-repro update                         refresh the cached lists from https://rb.zq1.de/compare.factory/
  tw-repro version

The data is Bernhard M. Wiedemann's rebuild of openSUSE:Factory on x86_64.
"reproduced" means the official RPMs were rebuilt from the same source on
another machine and came out bit-identical (GPG signature excluded).

Examples:
  tw-repro status
  tw-repro status --json > repro.json
  tw-repro verify --sample 5
  tw-repro verify bash coreutils
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tw-repro:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "status":
		return cmdStatus(ctx, args[1:])
	case "verify":
		return cmdVerify(ctx, args[1:])
	case "update":
		c := rb.NewClient()
		c.Log = os.Stderr
		return c.Update(ctx)
	case "version":
		fmt.Println("tw-repro", version)
		return nil
	}
	return fmt.Errorf("unknown command %q (try --help)", args[0])
}

func load(ctx context.Context) (*rb.Data, rb.Meta, []rb.Verdict, error) {
	pkgs, err := rpm.Installed(ctx)
	if err != nil {
		return nil, rb.Meta{}, nil, err
	}
	c := rb.NewClient()
	c.Log = os.Stderr
	data, meta, err := c.Load(ctx, false)
	if err != nil {
		return nil, rb.Meta{}, nil, err
	}
	vs := make([]rb.Verdict, 0, len(pkgs))
	for _, p := range pkgs {
		vs = append(vs, data.Classify(p))
	}
	return data, meta, vs, nil
}

func source(meta rb.Meta) string {
	s := fmt.Sprintf("comparison from %s", rb.DefaultBaseURL)
	if d := meta.ComparisonDate(); d != "" {
		s += " dated " + d
	}
	return s + fmt.Sprintf(", cached %s ago", meta.Age())
}

func cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the full result as JSON")
	all := fs.Bool("all", false, "also list the packages the rebuild does not cover")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, meta, vs, err := load(ctx)
	if err != nil {
		return err
	}
	sum := rb.Summarize(vs)
	if *asJSON {
		out := map[string]any{
			"source":          rb.DefaultBaseURL,
			"comparison_date": meta.ComparisonDate(),
			"cached_at":       meta.FetchedAt,
			"summary":         sum,
			"packages":        vs,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Println(sum)
	if sum.Stale > 0 {
		fmt.Printf("%d packages are installed at a different version than the one rebuilt; their verdict is about the rebuilt version.\n", sum.Stale)
	}
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "PACKAGE\tVERSION\tSOURCE\tVERDICT")
	for _, v := range vs {
		if v.Class == rb.Reproduced && !v.Differed {
			continue
		}
		if v.Class == rb.NotCovered && !*all {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s-%s\t%s\t%s\n", v.Name, v.Version, v.Release, v.OBSPackage, verdict(v))
	}
	tw.Flush()
	fmt.Println()
	fmt.Println(source(meta))
	if !*all && sum.NotCovered > 0 {
		fmt.Println("not covered packages are hidden; use --all to list them")
	}
	return nil
}

func verdict(v rb.Verdict) string {
	var parts []string
	switch v.Class {
	case rb.Reproduced:
		parts = append(parts, "reproduced")
	case rb.Equivalent:
		parts = append(parts, "equivalent, not bit-identical")
	case rb.VerifyFailed:
		parts = append(parts, "rebuild differs from the official binary")
	default:
		parts = append(parts, "not covered: "+v.Reason)
	}
	if v.Differed {
		if v.Minor {
			parts = append(parts, "two varied builds differ (minor)")
		} else {
			parts = append(parts, "two varied builds differ")
		}
	}
	if v.Stale {
		parts = append(parts, "rebuilt version "+v.RebuiltVersion)
	}
	return strings.Join(parts, "; ")
}

func cmdVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	sample := fs.Int("sample", 10, "number of random installed packages to check when none are named")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, meta, vs, err := load(ctx)
	if err != nil {
		return err
	}
	var pick []rb.Verdict
	if fs.NArg() > 0 {
		want := map[string]bool{}
		for _, n := range fs.Args() {
			want[n] = true
		}
		for _, v := range vs {
			if want[v.Name] {
				pick = append(pick, v)
				delete(want, v.Name)
			}
		}
		for n := range want {
			fmt.Fprintf(os.Stderr, "tw-repro: %s is not installed\n", n)
		}
	} else {
		var pool []rb.Verdict
		for _, v := range vs {
			if v.Class != rb.NotCovered && (v.Arch == "x86_64" || v.Arch == "noarch") {
				pool = append(pool, v)
			}
		}
		rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		if len(pool) > *sample {
			pool = pool[:*sample]
		}
		pick = pool
	}
	if len(pick) == 0 {
		return errors.New("nothing to verify")
	}
	fmt.Printf("checking %d packages against the rebuilder's checksums (%s)\n\n", len(pick), source(meta))
	f := repo.New()
	start := time.Now()
	identical, differ, missing := 0, 0, 0
	for _, v := range pick {
		file := rpm.FileName(v.Installed)
		if v.Arch != "x86_64" && v.Arch != "noarch" {
			fmt.Printf("%s: skipped, the rebuild only covers x86_64 and noarch packages\n", file)
			continue
		}
		entries := data.Checksums.ByFile[file]
		r, where, err := f.Open(ctx, v.Arch, file)
		if err != nil {
			fmt.Printf("%s: %v\n", file, err)
			continue
		}
		info, err := rpm.Digest(r)
		r.Close()
		if err != nil {
			fmt.Printf("%s: %v\n", file, err)
			continue
		}
		fmt.Printf("%s  (%s from %s)\n", file, rb.HumanBytes(info.Size), where)
		fmt.Printf("  md5 %s; without the signature %s\n", info.AsIs.MD5, info.Unsigned.MD5)
		if len(entries) == 0 {
			missing++
			fmt.Printf("  no rebuild checksum for this exact file (the rebuild pins the release to 1.1 and was of version %s)\n", v.RebuiltVersion)
		} else {
			match := false
			for _, e := range entries {
				if e.MD5 == info.AsIs.MD5 || e.MD5 == info.Unsigned.MD5 {
					match = true
				}
			}
			if match {
				identical++
				fmt.Printf("  bit-for-bit identical to the independent rebuild\n")
			} else {
				differ++
				fmt.Printf("  differs from the rebuild (%s/RPMS/: md5 %s)\n", entries[0].OBSPackage, entries[0].MD5)
			}
		}
		fmt.Printf("  rebuilder's own comparison with this official binary: %s\n\n", verdict(v))
	}
	fmt.Printf("%d identical, %d differ, %d without a comparable rebuild file; %s downloaded in %s\n",
		identical, differ, missing, rb.HumanBytes(f.Downloaded.Load()), time.Since(start).Round(time.Second))
	if differ > 0 {
		fmt.Println("note: the published checksums are of the rebuilder's double build, which pins the release to 1.1,")
		fmt.Println("carries no DISTURL header tag and keeps 4096 bytes of signature space; official RPMs differ in all three,")
		fmt.Println("so a byte match is not expected today. The rebuilder's verdict above comes from its own rebuild of the official source.")
	}
	return nil
}
