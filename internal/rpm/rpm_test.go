package rpm

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseInstalled(t *testing.T) {
	in := "zlib\t1.3.1\t1.1\tx86_64\tzlib-1.3.1-1.1.src.rpm\n" +
		"gpg-pubkey\t29b700a4\t62b07e22\t(none)\t(none)\n" +
		"bash-sh\t5.3.20\t10.2\tnoarch\tbash-5.3.20-10.2.src.rpm\n"
	pkgs, err := ParseInstalled(bytes.NewReader([]byte(in)))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 || pkgs[0].Name != "bash-sh" || pkgs[1].SourceRPM != "zlib-1.3.1-1.1.src.rpm" {
		t.Errorf("got %+v", pkgs)
	}
	if FileName(pkgs[0]) != "bash-sh-5.3.20-10.2.noarch.rpm" {
		t.Errorf("file name %s", FileName(pkgs[0]))
	}
}

// buildHeader serialises entries the way rpm does, for the synthetic file.
func buildHeader(entries []entry, regionAll bool) []byte {
	if regionAll {
		return export(append([]entry(nil), entries...))
	}
	var data bytes.Buffer
	var idx bytes.Buffer
	for _, e := range entries {
		if a := alignment(e.typ); data.Len()%a != 0 {
			data.Write(make([]byte, a-data.Len()%a))
		}
		binary.Write(&idx, binary.BigEndian, [4]uint32{e.tag, e.typ, uint32(data.Len()), e.count})
		data.Write(e.data)
	}
	var out bytes.Buffer
	out.Write(headerMagic)
	binary.Write(&out, binary.BigEndian, [2]uint32{uint32(len(entries)), uint32(data.Len())})
	out.Write(idx.Bytes())
	out.Write(data.Bytes())
	return out.Bytes()
}

func TestDigestSyntheticRPM(t *testing.T) {
	// Signed signature header: SHA256 string, RSA signature, SIZE int32,
	// reserved space. Removing the RSA tag must re-align SIZE and move the
	// region trailer, and leave the main header and payload untouched.
	signed := []entry{
		{tag: 273, typ: 6, count: 1, data: []byte("abc\x00")},
		{tag: 268, typ: typeBin, count: 5, data: []byte("SIGNA")},
		{tag: 1000, typ: 4, count: 1, data: []byte{0, 0, 0, 9}},
		{tag: 1008, typ: typeBin, count: 3, data: []byte{0, 0, 0}},
	}
	unsigned := []entry{signed[0], signed[2], signed[3]}
	main := buildHeader([]entry{
		{tag: tagBuildHost, typ: 6, count: 1, data: []byte("reproducible\x00")},
		{tag: tagDistURL, typ: 6, count: 1, data: []byte("obs://x\x00")},
	}, false)
	file := func(sig []entry) []byte {
		var b bytes.Buffer
		lead := make([]byte, leadSize)
		copy(lead, []byte{0xed, 0xab, 0xee, 0xdb})
		b.Write(lead)
		sh := buildHeader(sig, true)
		b.Write(sh)
		b.Write(make([]byte, pad8(len(sh))))
		b.Write(main)
		b.WriteString("payload")
		return b.Bytes()
	}
	got, err := Digest(bytes.NewReader(file(signed)))
	if err != nil {
		t.Fatal(err)
	}
	want, err := Digest(bytes.NewReader(file(unsigned)))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Signed || want.Signed {
		t.Errorf("signed flags: %v %v", got.Signed, want.Signed)
	}
	if got.Unsigned != want.AsIs || got.AsIs == got.Unsigned {
		t.Errorf("stripped digests %+v, want %+v", got.Unsigned, want.AsIs)
	}
	if got.DistURL != "obs://x" || got.BuildHost != "reproducible" || got.ReservedSpace != 3 {
		t.Errorf("tags: %+v", got)
	}
	if _, err := Digest(bytes.NewReader([]byte("not an rpm at all, really not"))); err == nil {
		t.Error("garbage accepted")
	}
}

// TestDelsignAgainstRPM compares the stripper with real "rpm --delsign"
// output when TW_REPRO_RPMS points at a directory of official RPMs and a
// delsign.txt of "<file> <md5 after rpm --delsign>" lines.
func TestDelsignAgainstRPM(t *testing.T) {
	dir := os.Getenv("TW_REPRO_RPMS")
	if dir == "" {
		t.Skip("TW_REPRO_RPMS not set")
	}
	f, err := os.Open(filepath.Join(dir, "delsign.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, want, _ := strings.Cut(sc.Text(), " ")
		r, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		info, err := Digest(r)
		r.Close()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if info.Unsigned.MD5 != want {
			t.Errorf("%s: stripped md5 %s, rpm --delsign gives %s", name, info.Unsigned.MD5, want)
		}
	}
}
