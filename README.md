# tw-repro

Arch has arch-repro-status and Debian has debian-repro-status: one command that
says how many packages on this machine were rebuilt bit-for-bit by someone else.
openSUSE publishes the data (Bernhard M. Wiedemann's rebuild of Factory at
<https://rb.zq1.de/compare.factory/>) but had no client. tw-repro is that client.

```
$ tw-repro status
137 packages: 76.6% of the 107 covered by the rebuild are reproduced bit-for-bit, 14 equivalent, 11 failed verification, 30 not covered; 0 differ between the rebuilder's two varied builds
PACKAGE        VERSION     SOURCE   VERDICT
busybox        1.38.0-4.1  busybox  rebuild differs from the official binary
grep           3.12-1.5    grep     equivalent, not bit-identical
libnghttp2-14  1.71.0-1.1  nghttp2  rebuild differs from the official binary; rebuilt version 1.70.0
... (25 rows)

comparison from https://rb.zq1.de/compare.factory dated 2026-11-03, cached 0s ago
$ tw-repro verify --sample 5
p11-kit-tools-0.26.5-1.1.x86_64.rpm  (150 kB from https://download.opensuse.org/tumbleweed/repo/oss/x86_64/...)
  differs from the rebuild (p11-kit/RPMS/: md5 85d9b470ae647af752c13e473902fe79)
  rebuilder's own comparison with this official binary: rebuild differs from the official binary
tar-1.35-10.1.x86_64.rpm  (237 kB from https://download.opensuse.org/tumbleweed/repo/oss/x86_64/...)
  no rebuild checksum for this exact file (the rebuild pins the release to 1.1 and was of version 1.35)
  rebuilder's own comparison with this official binary: reproduced
0 identical, 1 differ, 4 without a comparable rebuild file; 723 kB downloaded in 5s
```

## How it works

Rules come from <https://github.com/bmwiedemann/reproducibleopensuse> (`rbk`,
`rebuild`, `nachbau`, `jsonresult`, `rbstats`) and <https://rb.zq1.de/spec/glossar.txt>.
Per source package the rebuilder builds twice locally with a different date,
hostname and CPU count (`status` in `reproducible.json`; `differed-builds.txt`
= the 163 where the builds differ), and rebuilds the exact source of the
official binary, strips the GPG signature from the official RPMs and compares
(`verified`: 2 bit-identical, 1 equivalent for build-compare, 0 different;
`build-verify-failed.txt` = the 1,049 with 0). `checksums.txt` holds md5 and
sha256 of the two local builds' RPMs (`RPMS.2/` first, `RPMS/` second) keyed by
OBS package, and is the only published map from source RPM to OBS package.

`status` reads `rpm -qa`, maps each package to its OBS package via the source
RPM and classifies: **reproduced** (verified 2), **equivalent** (1), **failed
verification** (0), **not covered** (source not in the comparison, rebuild
failed or skipped, or the rebuild never produced this subpackage, e.g. an
unbuilt multibuild flavour). "differ" counts packages whose two varied builds
disagree, independent of the verdict. Lists are cached a day under
`$XDG_CACHE_HOME/tw-repro/`; `update` refreshes.

## Install

`go install github.com/officialasishkumar/tw-repro@latest` (needs `rpm`; standard library only).

## Commands

`status [--all] [--json]`, `verify [--sample N | PKG...]` (download the official
RPM, hash it, compare), `update` (refresh the cached lists).

## Limits

- `verify` cannot produce a byte match today, and says so. The published
  checksums are of the double build, which pins the release to `1.1`, has no
  DISTURL tag and keeps 4096 bytes of reserved signature space; official RPMs
  have a DISTURL, the real release counter and 3024 bytes after signing. 14
  official RPMs with matching names were checked, as is and after a Go
  re-implementation of `rpm --delsign` (validated byte-for-byte against rpm
  4.20.1): none matched. The bit-for-bit statement therefore rests on the
  rebuilder's own comparison, published only as the `verified` flag.
- The rebuild is x86_64 only (`verify` covers x86_64 and noarch; on aarch64
  the verdicts are about the x86_64 build). It is a dated snapshot of Factory:
  newer installed builds are marked "rebuilt version X" and the mirror may no
  longer carry the file. The rebuilder's November report says "failed
  verification" is inflated by about 300 packages because tests were not clean.

## Related work

arch-repro-status and debian-repro-status read a rebuilderd instance
(reproduce.debian.net is Debian's). openSUSE has no rebuilderd, so tw-repro
reads rb.zq1.de directly. Background:
<https://news.opensuse.org/2024/04/18/factory-bit-reproducible-builds/>.

## License

MIT
