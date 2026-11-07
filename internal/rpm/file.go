package rpm

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"sort"
)

// An RPM file is a 96-byte lead, a signature header padded to 8 bytes, the
// main header and the payload. Both headers share one format: 8 bytes of
// magic, the number of index entries, the size of the data block, the 16-byte
// index entries (tag, type, offset, count) and the data block.
var headerMagic = []byte{0x8e, 0xad, 0xe8, 0x01, 0, 0, 0, 0}

const (
	leadSize    = 96
	regionTag   = 62 // RPMTAG_HEADERSIGNATURES
	typeBin     = 7
	regionCount = 16
)

// Signature tags that "rpm --delsign" removes (rpmgensig.c, deleteSigs).
var signatureTags = map[uint32]bool{
	267:  true, // RPMSIGTAG_DSA
	268:  true, // RPMSIGTAG_RSA
	278:  true, // RPMSIGTAG_OPENPGP
	1002: true, // RPMSIGTAG_PGP
	1005: true, // RPMSIGTAG_GPG
	1006: true, // RPMSIGTAG_PGP5
}

// Tags tw-repro reads from the main header.
const (
	tagBuildHost = 1007
	tagDistURL   = 1123
)

type entry struct {
	tag, typ, count uint32
	data            []byte
}

// Header is a parsed RPM header.
type Header struct {
	entries []entry
	size    int // bytes consumed from the file, including the magic
}

func readHeader(r io.Reader) (*Header, error) {
	var pre [16]byte
	if _, err := io.ReadFull(r, pre[:]); err != nil {
		return nil, err
	}
	if !bytes.Equal(pre[:8], headerMagic) {
		return nil, errors.New("bad header magic")
	}
	n := binary.BigEndian.Uint32(pre[8:12])
	dlen := binary.BigEndian.Uint32(pre[12:16])
	if n > 1<<16 || dlen > 1<<28 {
		return nil, errors.New("implausible header size")
	}
	idx := make([]byte, 16*n)
	if _, err := io.ReadFull(r, idx); err != nil {
		return nil, err
	}
	data := make([]byte, dlen)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	h := &Header{size: 16 + len(idx) + len(data)}
	for i := 0; i < int(n); i++ {
		e := entry{
			tag:   binary.BigEndian.Uint32(idx[16*i:]),
			typ:   binary.BigEndian.Uint32(idx[16*i+4:]),
			count: binary.BigEndian.Uint32(idx[16*i+12:]),
		}
		off := binary.BigEndian.Uint32(idx[16*i+8:])
		if off > dlen {
			return nil, fmt.Errorf("tag %d: offset outside the data block", e.tag)
		}
		size, err := dataSize(e.typ, e.count, data[off:])
		if err != nil {
			return nil, fmt.Errorf("tag %d: %w", e.tag, err)
		}
		e.data = data[off : off+size]
		h.entries = append(h.entries, e)
	}
	return h, nil
}

func dataSize(typ, count uint32, data []byte) (uint32, error) {
	var size uint64
	switch typ {
	case 0: // NULL
	case 1, 2, 7: // CHAR, INT8, BIN
		size = uint64(count)
	case 3:
		size = 2 * uint64(count)
	case 4:
		size = 4 * uint64(count)
	case 5:
		size = 8 * uint64(count)
	case 6, 8, 9: // STRING, STRING_ARRAY, I18NSTRING: count NUL-terminated strings
		if typ == 6 {
			count = 1
		}
		for i := uint32(0); i < count; i++ {
			j := bytes.IndexByte(data[size:], 0)
			if j < 0 {
				return 0, errors.New("unterminated string")
			}
			size += uint64(j) + 1
		}
	default:
		return 0, fmt.Errorf("unknown type %d", typ)
	}
	if size > uint64(len(data)) {
		return 0, errors.New("data outside the block")
	}
	return uint32(size), nil
}

func alignment(typ uint32) int {
	switch typ {
	case 3:
		return 2
	case 4:
		return 4
	case 5:
		return 8
	}
	return 1
}

// String returns a string tag from the header.
func (h *Header) String(tag uint32) string {
	for _, e := range h.entries {
		if e.tag == tag && e.typ == 6 {
			return string(bytes.TrimRight(e.data, "\x00"))
		}
	}
	return ""
}

// Count returns the count field of a tag, or 0.
func (h *Header) Count(tag uint32) int {
	for _, e := range h.entries {
		if e.tag == tag {
			return int(e.count)
		}
	}
	return 0
}

// HasSignature reports whether any signature tag is present.
func (h *Header) HasSignature() bool {
	for _, e := range h.entries {
		if signatureTags[e.tag] {
			return true
		}
	}
	return false
}

// withoutSignature returns the header serialised the way rpm 4.20 writes it
// after "rpm --delsign": signature tags dropped, every remaining tag in one
// region sorted by tag number, data aligned per type, region trailer last.
// The reserved space (tag 1008) keeps its post-signing size, as rpm does.
func (h *Header) withoutSignature() []byte {
	var keep []entry
	for _, e := range h.entries {
		if e.tag != regionTag && !signatureTags[e.tag] {
			keep = append(keep, e)
		}
	}
	return export(keep)
}

// export serialises entries as one region header, as rpm's headerExport does.
func export(keep []entry) []byte {
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].tag < keep[j].tag })
	n := len(keep) + 1
	var data bytes.Buffer
	idx := make([]byte, 0, 16*n)
	put := func(tag, typ, off, count uint32) {
		var b [16]byte
		binary.BigEndian.PutUint32(b[0:], tag)
		binary.BigEndian.PutUint32(b[4:], typ)
		binary.BigEndian.PutUint32(b[8:], off)
		binary.BigEndian.PutUint32(b[12:], count)
		idx = append(idx, b[:]...)
	}
	// The region entry comes first and points at the trailer, which is
	// written after all other data; patch its offset once that is known.
	put(regionTag, typeBin, 0, regionCount)
	for _, e := range keep {
		if a := alignment(e.typ); data.Len()%a != 0 {
			data.Write(make([]byte, a-data.Len()%a))
		}
		put(e.tag, e.typ, uint32(data.Len()), e.count)
		data.Write(e.data)
	}
	binary.BigEndian.PutUint32(idx[8:], uint32(data.Len()))
	var trailer [16]byte
	binary.BigEndian.PutUint32(trailer[0:], regionTag)
	binary.BigEndian.PutUint32(trailer[4:], typeBin)
	binary.BigEndian.PutUint32(trailer[8:], uint32(-int32(16*n)))
	binary.BigEndian.PutUint32(trailer[12:], regionCount)
	data.Write(trailer[:])

	out := bytes.NewBuffer(make([]byte, 0, 16+len(idx)+data.Len()+8))
	out.Write(headerMagic)
	var cnt [8]byte
	binary.BigEndian.PutUint32(cnt[0:], uint32(n))
	binary.BigEndian.PutUint32(cnt[4:], uint32(data.Len()))
	out.Write(cnt[:])
	out.Write(idx)
	out.Write(data.Bytes())
	return out.Bytes()
}

// Digests of one byte stream.
type Digests struct {
	MD5    string `json:"md5"`
	SHA256 string `json:"sha256"`
}

type digester struct {
	md5, sha hash.Hash
	w        io.Writer
}

func newDigester() *digester {
	d := &digester{md5: md5.New(), sha: sha256.New()}
	d.w = io.MultiWriter(d.md5, d.sha)
	return d
}

func (d *digester) sum() Digests {
	return Digests{hex.EncodeToString(d.md5.Sum(nil)), hex.EncodeToString(d.sha.Sum(nil))}
}

// Info is what Digest learns about an RPM file.
type Info struct {
	Size int64 `json:"size"`
	// AsIs digests the file as downloaded.
	AsIs Digests `json:"as_is"`
	// Unsigned digests the file with the GPG signature removed the way
	// "rpm --delsign" does it; equal to AsIs when the file is unsigned.
	Unsigned      Digests `json:"unsigned"`
	Signed        bool    `json:"signed"`
	ReservedSpace int     `json:"reserved_space"` // bytes of RPMSIGTAG_RESERVEDSPACE
	DistURL       string  `json:"disturl,omitempty"`
	BuildHost     string  `json:"buildhost,omitempty"`
}

func pad8(n int) int { return (8 - n%8) % 8 }

// Digest reads an RPM file once and computes both sets of digests.
func Digest(r io.Reader) (Info, error) {
	var info Info
	lead := make([]byte, leadSize)
	if _, err := io.ReadFull(r, lead); err != nil {
		return info, fmt.Errorf("reading lead: %w", err)
	}
	if !bytes.Equal(lead[:4], []byte{0xed, 0xab, 0xee, 0xdb}) {
		return info, errors.New("not an RPM file")
	}
	// Re-read the signature header bytes so the as-is digest sees them.
	var sigRaw bytes.Buffer
	sig, err := readHeader(io.TeeReader(r, &sigRaw))
	if err != nil {
		return info, fmt.Errorf("signature header: %w", err)
	}
	padding := make([]byte, pad8(sig.size))
	if _, err := io.ReadFull(r, padding); err != nil {
		return info, fmt.Errorf("signature padding: %w", err)
	}
	info.Signed = sig.HasSignature()
	info.ReservedSpace = sig.Count(1008)

	asIs, unsigned := newDigester(), newDigester()
	asIs.w.Write(lead)
	asIs.w.Write(sigRaw.Bytes())
	asIs.w.Write(padding)
	unsigned.w.Write(lead)
	if info.Signed {
		stripped := sig.withoutSignature()
		unsigned.w.Write(stripped)
		unsigned.w.Write(make([]byte, pad8(len(stripped))))
	} else {
		unsigned.w.Write(sigRaw.Bytes())
		unsigned.w.Write(padding)
	}
	// The main header and payload are identical in both forms.
	rest := io.MultiWriter(asIs.w, unsigned.w)
	var hdrRaw bytes.Buffer
	hdr, err := readHeader(io.TeeReader(r, &hdrRaw))
	if err != nil {
		return info, fmt.Errorf("main header: %w", err)
	}
	rest.Write(hdrRaw.Bytes())
	info.DistURL = hdr.String(tagDistURL)
	info.BuildHost = hdr.String(tagBuildHost)
	n, err := io.Copy(rest, r)
	if err != nil {
		return info, err
	}
	info.Size = int64(leadSize+sigRaw.Len()+len(padding)+hdrRaw.Len()) + n
	info.AsIs, info.Unsigned = asIs.sum(), unsigned.sum()
	return info, nil
}
