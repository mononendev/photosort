package exif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mononendev/photosort/internal/py"
)

// This file reproduces exifread.process_file(f, details=False) (exifread 3.5.1) closely enough that the tags read()
// looks at come out identical, including the quirks: how it hunts for the TIFF header in each container, that it
// treats any byte-order mark other than "I" as big-endian, that out-of-range reads yield 0 instead of failing, and
// that any exception (which Python's read() swallowed) sends the caller to the Pillow fallback. Only the tags read()
// asks for are decoded; the rest are walked just far enough to know whether exifread would have returned anything.

var errExifread = errors.New("exifread failed")

// file is random access over the whole file, with Python's short-read semantics.
type file struct {
	r    io.ReaderAt
	size int64
}

// at returns up to n bytes at off (fewer at EOF, none past it).
func (f *file) at(off, n int64) []byte {
	if off < 0 || n <= 0 || off >= f.size {
		return nil
	}
	if n > f.size-off {
		n = f.size - off
	}
	b := make([]byte, n)
	k, _ := f.r.ReadAt(b, off)
	return b[:k]
}

// cursor is a Python file object: seek anywhere, read what's there.
type cursor struct {
	f   *file
	pos int64
}

// read is fh.read(n); a negative n reads to EOF.
func (c *cursor) read(n int64) []byte {
	if n < 0 {
		n = c.f.size - c.pos
	}
	b := c.f.at(c.pos, n)
	c.pos += int64(len(b))
	return b
}

// slice is Python's data[i:j] (clamped, never failing).
func slice(b []byte, i, j int) []byte {
	if i < 0 {
		i = 0
	}
	if j > len(b) {
		j = len(b)
	}
	if i >= j {
		return nil
	}
	return b[i:j]
}

// determineType finds the TIFF header: its offset in the file and the byte-order mark (exifread's determine_type).
func determineType(f *file) (int64, []byte, error) {
	c := &cursor{f: f}
	data := c.read(12)
	switch {
	case len(data) >= 2 && (string(data[:2]) == "II" || string(data[:2]) == "MM"):
		c.pos = 0
		return 0, c.read(1), nil
	case isOneOf(slice(data, 4, 12), "ftypheic", "ftypavif", "ftypmif1"):
		c.pos = 0
		off, endian, err := heicFindExif(c)
		if err != nil {
			return 0, nil, err
		}
		if off == 0 {
			return heicFindTIFF(c)
		}
		return off, endian, nil
	case string(slice(data, 0, 4)) == "RIFF" && string(slice(data, 8, 12)) == "WEBP":
		return webpFindExif(c)
	case string(slice(data, 0, 2)) == "\xff\xd8":
		return jpegFindExif(c, data)
	case string(slice(data, 0, 8)) == "\x89PNG\r\n\x1a\n":
		return pngFindExif(c)
	}
	// JPEG XL (which exifread also reads) is not a format photosort scans.
	return 0, nil, errExifread
}

func isOneOf(b []byte, opts ...string) bool {
	for _, o := range opts {
		if string(b) == o {
			return true
		}
	}
	return false
}

func webpFindExif(c *cursor) (int64, []byte, error) {
	data := c.read(5)
	if string(slice(data, 0, 4)) != "VP8X" || len(data) < 5 || data[4]&8 == 0 {
		return 0, nil, errExifread
	}
	c.pos += 13
	for {
		data = c.read(8)
		if len(data) != 8 {
			return 0, nil, errExifread
		}
		if string(data[:4]) == "EXIF" {
			c.pos += 6 // exifread assumes an "Exif\0\0" prefix on the chunk payload
			off := c.pos
			return off, c.read(1), nil
		}
		c.pos += int64(binary.LittleEndian.Uint32(data[4:8]))
	}
}

func pngFindExif(c *cursor) (int64, []byte, error) {
	c.pos = 8
	for {
		data := c.read(8)
		chunk := slice(data, 4, 8)
		for _, b := range chunk {
			if b >= 0x80 { // the chunk name is ASCII-decoded for a debug log, which raises
				return 0, nil, errExifread
			}
		}
		if len(chunk) == 0 || string(chunk) == "IEND" {
			return 0, nil, errExifread
		}
		if string(chunk) == "eXIf" {
			off := c.pos
			return off, c.read(1), nil
		}
		var size int64
		for _, b := range slice(data, 0, 4) {
			size = size<<8 | int64(b)
		}
		c.pos += size + 4
	}
}

// jpegFindExif follows exifread's segment walk, which only looks at the first few KB and gives up on anything odd.
func jpegFindExif(c *cursor, data []byte) (int64, []byte, error) {
	base := 2
	for {
		if len(data) <= 2 {
			return 0, nil, errExifread
		}
		if data[2] != 0xFF || !isOneOf(slice(data, 6, 10), "JFIF", "JFXX", "OLYM", "Phot") {
			break
		}
		length := int(data[4])*256 + int(data[5])
		c.read(int64(length - 8))
		data = append([]byte{0xFF, 0x00}, c.read(10)...)
		if base > 2 {
			base = base + length + 4 - 2
		} else {
			base = length + 4
		}
	}
	c.pos = 0
	data = c.read(int64(base + 4000))
	incr := func() bool {
		if base+3 >= len(data) {
			return false
		}
		base += int(data[base+2])*256 + int(data[base+3]) + 2
		return true
	}
walk:
	for {
		switch string(slice(data, base, base+2)) {
		case "\xff\xe1":
			if string(slice(data, base+4, base+8)) == "Exif" {
				base -= 2
				break walk
			}
			if !incr() {
				return 0, nil, errExifread
			}
		case "\xff\xdb":
			break walk
		default: // APP0, APP2, APP14, FFD8, APP12 and anything else all just step over the segment
			if !incr() {
				return 0, nil, errExifread
			}
		}
	}
	c.pos = int64(base + 12)
	if base+2 < 0 || base+2 >= len(data) || data[base+2] != 0xFF {
		return 0, nil, errExifread
	}
	if string(slice(data, base+6, base+10)) == "Exif" || isOneOf(slice(data, base+6, base+11), "Ducky", "Adobe") {
		off := c.pos
		return off, c.read(1), nil
	}
	return 0, nil, errExifread
}

// --- HEIC (exifread's HEICExifFinder) ---

type box struct {
	name       string
	size, pos  int64
	after      int64
	version    int
	itemID     int64
	itemType   string
	exifInfe   *box
	locs       map[int64][][2]int64
	majorBrand string
	minor      uint32
}

type heic struct {
	c    *cursor
	subs map[string]*box
}

func (h *heic) get(n int64) ([]byte, error) {
	b := h.c.read(n)
	if len(b) == 0 || int64(len(b)) != n {
		return nil, errExifread
	}
	return b, nil
}

func (h *heic) getN(size int) (int64, error) {
	switch size {
	case 0:
		return 0, nil
	case 2, 4, 8:
		b, err := h.get(int64(size))
		if err != nil {
			return 0, err
		}
		var v uint64
		for _, x := range b {
			v = v<<8 | uint64(x)
		}
		return int64(v), nil
	}
	return 0, errExifread
}

func (h *heic) nextBox() (*box, error) {
	pos := h.c.pos
	size, err := h.getN(4)
	if err != nil {
		return nil, err
	}
	kind, err := h.get(4)
	if err != nil {
		return nil, err
	}
	for _, b := range kind {
		if b >= 0x80 {
			return nil, errExifread
		}
	}
	bx := &box{name: string(kind)}
	switch size {
	case 0:
		return nil, errExifread
	case 1:
		if size, err = h.getN(8); err != nil {
			return nil, err
		}
		bx.size, bx.after = size-16, pos+size
	default:
		bx.size, bx.after = size-8, pos+size
	}
	if bx.after <= pos { // Python would loop forever on a zero-length 64-bit box
		return nil, errExifread
	}
	bx.pos = h.c.pos
	return bx, nil
}

func (h *heic) full(bx *box) error {
	v, err := h.getN(4)
	bx.version = int(v >> 24)
	return err
}

func (h *heic) expectParse(name string) (*box, error) {
	for {
		bx, err := h.nextBox()
		if err != nil {
			return nil, err
		}
		if bx.name == name {
			if p := h.parser(bx.name); p != nil {
				if err := p(bx); err != nil {
					return nil, err
				}
			}
			h.c.pos = bx.after
			return bx, nil
		}
		h.c.pos = bx.after
	}
}

func (h *heic) parser(name string) func(*box) error {
	switch name {
	case "ftyp":
		return h.parseFtyp
	case "meta":
		return h.parseMeta
	case "infe":
		return h.parseInfe
	case "iinf":
		return h.parseIinf
	case "iloc":
		return h.parseIloc
	case "hdlr", "pitm", "iref", "idat", "dinf", "iprp":
		return func(*box) error { return nil }
	}
	return nil
}

func (h *heic) parseFtyp(bx *box) error {
	b, err := h.get(4)
	if err != nil {
		return err
	}
	bx.majorBrand = string(b)
	m, err := h.getN(4)
	if err != nil {
		return err
	}
	bx.minor = uint32(m)
	for size := bx.size - 8; size > 0; size -= 4 {
		if _, err := h.get(4); err != nil {
			return err
		}
	}
	return nil
}

func (h *heic) parseMeta(meta *box) error {
	if err := h.full(meta); err != nil {
		return err
	}
	for h.c.pos < meta.after {
		bx, err := h.nextBox()
		if err != nil {
			return err
		}
		if p := h.parser(bx.name); p != nil {
			if err := p(bx); err != nil {
				return err
			}
			h.subs[bx.name] = bx
		}
		h.c.pos = bx.after
	}
	return nil
}

func (h *heic) parseInfe(bx *box) error {
	if err := h.full(bx); err != nil {
		return err
	}
	if bx.version < 2 {
		return nil
	}
	var err error
	switch bx.version {
	case 2:
		bx.itemID, err = h.getN(2)
	case 3:
		bx.itemID, err = h.getN(4)
	}
	if err != nil {
		return err
	}
	if _, err := h.getN(2); err != nil {
		return err
	}
	t, err := h.get(4)
	if err != nil {
		return err
	}
	bx.itemType = string(t)
	for { // item name: NUL-terminated
		b, err := h.get(1)
		if err != nil {
			return err
		}
		if b[0] == 0 {
			return nil
		}
	}
}

func (h *heic) parseIinf(bx *box) error {
	if err := h.full(bx); err != nil {
		return err
	}
	count, err := h.getN(2) // exifread reads 16 bits whatever the version
	if err != nil {
		return err
	}
	for range count {
		infe, err := h.expectParse("infe")
		if err != nil {
			return err
		}
		if infe.itemType == "Exif" {
			bx.exifInfe = infe
			break
		}
	}
	return nil
}

func (h *heic) parseIloc(bx *box) error {
	if err := h.full(bx); err != nil {
		return err
	}
	s, err := h.get(2)
	if err != nil {
		return err
	}
	offSize, lenSize, baseSize, idxSize := int(s[0]>>4), int(s[0]&15), int(s[1]>>4), int(s[1]&15)
	var count int64
	switch {
	case bx.version < 2:
		count, err = h.getN(2)
	case bx.version == 2:
		count, err = h.getN(4)
	default:
		return errExifread
	}
	if err != nil {
		return err
	}
	bx.locs = map[int64][][2]int64{}
	for range count {
		idSize := 2
		if bx.version == 2 {
			idSize = 4
		}
		id, err := h.getN(idSize)
		if err != nil {
			return err
		}
		skip := 2 // data_reference_index
		if bx.version == 1 || bx.version == 2 {
			skip = 4 // plus construction_method
		}
		if _, err := h.get(int64(skip)); err != nil {
			return err
		}
		if _, err := h.getN(baseSize); err != nil { // base_offset: read, then ignored by exifread
			return err
		}
		n, err := h.getN(2)
		if err != nil {
			return err
		}
		var extents [][2]int64
		for range n {
			if (bx.version == 1 || bx.version == 2) && idxSize > 0 {
				if _, err := h.getN(idxSize); err != nil {
					return err
				}
			}
			off, err := h.getN(offSize)
			if err != nil {
				return err
			}
			ln, err := h.getN(lenSize)
			if err != nil {
				return err
			}
			extents = append(extents, [2]int64{off, ln})
		}
		bx.locs[id] = extents
	}
	return nil
}

// heicFindExif is HEICExifFinder.find_exif: (0, nil) means "look for a bare TIFF header next".
func heicFindExif(c *cursor) (int64, []byte, error) {
	h := &heic{c: c, subs: map[string]*box{}}
	ftyp, err := h.expectParse("ftyp")
	if err != nil {
		return 0, nil, err
	}
	if !isOneOf([]byte(ftyp.majorBrand), "heic", "avif", "mif1") || ftyp.minor != 0 {
		return 0, nil, nil
	}
	if _, err := h.expectParse("meta"); err != nil {
		return 0, nil, err
	}
	iinf := h.subs["iinf"]
	if iinf == nil {
		return 0, nil, errExifread
	}
	if iinf.exifInfe == nil {
		return 0, nil, nil
	}
	iloc := h.subs["iloc"]
	if iloc == nil {
		return 0, nil, errExifread
	}
	extents, ok := iloc.locs[iinf.exifInfe.itemID]
	if !ok || len(extents) != 1 {
		return 0, nil, errExifread
	}
	c.pos = extents[0][0]
	hdr, err := h.getN(4)
	if err != nil {
		return 0, nil, err
	}
	if hdr == 0 {
		return 0, nil, nil
	}
	if hdr < 6 {
		return 0, nil, errExifread
	}
	b, err := h.get(hdr)
	if err != nil || string(b[len(b)-6:]) != "Exif\x00\x00" {
		return 0, nil, errExifread
	}
	off := c.pos
	return off, c.read(1), nil
}

// heicFindTIFF is find_heic_tiff: a bare little-endian TIFF header right where the cursor is.
func heicFindTIFF(c *cursor) (int64, []byte, error) {
	data := c.read(4)
	if len(data) < 2 || (string(data[:2]) != "II" && string(data[:2]) != "MM") || len(data) < 4 || data[2] != 42 || data[3] != 0 {
		return 0, nil, errExifread
	}
	return c.pos - 4, data[:2], nil
}

// --- the IFD walk (exifread's ExifHeader) ---

const (
	tASCII  = 2
	tRatio  = 5
	tSRatio = 10
	tFloat  = 11
	tDouble = 12
)

var typeLen = [14]int64{0, 1, 1, 2, 4, 8, 1, 1, 2, 4, 8, 4, 8, 4}

func signedType(t int) bool { return t == 6 || t == 8 || t == 9 || t == 10 }

// ratio is exifread's Ratio: a Fraction, except that a zero denominator is kept as is.
type ratio struct{ num, den int64 }

func newRatio(n, d int64) ratio {
	if d == 0 {
		return ratio{n, d}
	}
	if d < 0 {
		n, d = -n, -d
	}
	g := gcd(abs(n), d)
	if g > 1 {
		n, d = n/g, d/g
	}
	return ratio{n, d}
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs(a int64) int64 {
	if a < 0 {
		return -a
	}
	return a
}

func (r ratio) String() string {
	if r.den == 1 {
		return strconv.FormatInt(r.num, 10)
	}
	return fmt.Sprintf("%d/%d", r.num, r.den)
}

// floatTuple is the 1-tuple struct.unpack returns for a float field: str() prints "(1.5,)".
type floatTuple float64

type header struct {
	f      *file
	little bool
	offset int64
	tags   map[string]*tagVal // only the tags read() asks for
	any    bool               // whether exifread would have returned a non-empty dict
}

// tagVal is what read() needs from a tag: _num(tag) and str(tag).
type tagVal struct {
	num   float64
	numOK bool
	str   string
}

func (h *header) s2n(off int64, n int, signed bool) (int64, error) {
	abs := h.offset + off
	if abs < 0 {
		return 0, errExifread // Python: negative seek
	}
	b := h.f.at(abs, int64(n))
	if len(b) != n {
		return 0, nil
	}
	var u uint64
	if h.little {
		for i := n - 1; i >= 0; i-- {
			u = u<<8 | uint64(b[i])
		}
	} else {
		for _, x := range b {
			u = u<<8 | uint64(x)
		}
	}
	if signed {
		shift := 64 - 8*n
		return int64(u<<shift) >> shift, nil
	}
	return int64(u), nil
}

// u is s2n for offsets computed from values already read, where a negative position can't arise.
func (h *header) u(off int64, n int) int64 {
	v, _ := h.s2n(off, n, false)
	return v
}

func (h *header) listIFD() []int64 {
	var ifds []int64
	seen := map[int64]bool{}
	for i := h.u(4, 4); i != 0 && !seen[i] && len(ifds) < 10000; {
		seen[i] = true
		ifds = append(ifds, i)
		next := h.u(i+2+12*h.u(i, 2), 4)
		if next == i {
			next = 0
		}
		i = next
	}
	return ifds
}

// wanted maps the IFD name and tag number read() looks at to exifread's key for it.
var wanted = map[string]map[int64]string{
	"Image": {0x0110: "Image Model", 0x0132: "Image DateTime", 0x8769: "Image ExifOffset"},
	"EXIF": {0x829A: "EXIF ExposureTime", 0x829D: "EXIF FNumber", 0x8827: "EXIF ISOSpeedRatings",
		0x920A: "EXIF FocalLength", 0xA405: "EXIF FocalLengthIn35mmFilm", 0xA434: "EXIF LensModel",
		0x9003: "EXIF DateTimeOriginal"},
}

// ignored are the tags exifread skips when details=False.
var ignored = map[int64]bool{0x02BC: true, 0x927C: true, 0x9286: true}

// exifOffset is values[0] of "Image ExifOffset", when it is an int exifread would seek to.
type exifOffset struct {
	off   int64
	ok    bool
	empty bool
}

func (h *header) dumpIFD(ifd int64, name string, eo *exifOffset) error {
	n, err := h.s2n(ifd, 2, false)
	if err != nil {
		return err
	}
	want := wanted[name]
	for i := range n {
		entry := ifd + 2 + 12*i
		tag := h.u(entry, 2)
		if ignored[tag] {
			continue
		}
		if err := h.processTag(entry, tag, want[tag], eo); err != nil {
			return err
		}
	}
	return nil
}

func (h *header) processTag(entry, tag int64, key string, eo *exifOffset) error {
	ft := int(h.u(entry+2, 2))
	if ft < 1 || ft > 13 {
		return nil
	}
	tl := typeLen[ft]
	count := h.u(entry+4, 4)
	off := entry + 8
	if count*tl > 4 {
		off = h.u(off, 4)
	}
	h.any = true
	if key == "" {
		// Not a tag read() uses; exifread only fails on it when a lone float can't be read.
		if count == 1 && (ft == tFloat || ft == tDouble) && int64(len(h.f.at(h.offset+off, tl))) != tl {
			return errExifread
		}
		return nil
	}
	if ft == tASCII {
		s, raw, isStr := h.ascii(count, off)
		tv := &tagVal{}
		if isStr {
			tv.str = s
			tv.num, tv.numOK = pyFloat(s)
		} else if count > 50 && len(raw) > 20 {
			r := pyBytesRepr(raw[:20])
			tv.str = r[:len(r)-1] + ", ... ]"
		} else {
			tv.str = pyBytesRepr(raw)
		}
		if tv.numOK && (math.IsNaN(tv.num) || math.IsInf(tv.num, 0)) {
			tv.numOK = false // not representable in the stored JSON
		}
		h.tags[key] = tv
		if key == "Image ExifOffset" {
			switch {
			case isStr && s == "":
				*eo = exifOffset{empty: true}
			case isStr:
				*eo = exifOffset{} // a str offset raises TypeError, which exifread catches
			default:
				*eo = exifOffset{off: int64(raw[0]), ok: true}
			}
		}
		return nil
	}
	values, err := h.field(count, ft, tl, off)
	if err != nil {
		return err
	}
	if count == 1 && len(values) == 0 {
		return errExifread // IndexError on values[0]
	}
	tv := &tagVal{}
	switch {
	case count == 1:
		tv.str = pyRepr(values[0])
	case count > 50 && len(values) > 20:
		r := pyListRepr(values[:20])
		tv.str = r[:len(r)-1] + ", ... ]"
	default:
		tv.str = pyListRepr(values)
	}
	if len(values) > 0 {
		switch v := values[0].(type) {
		case int64:
			tv.num, tv.numOK = float64(v), true
		case ratio:
			if v.den != 0 {
				tv.num, tv.numOK = float64(v.num)/float64(v.den), true
			}
		case floatTuple:
			tv.num, tv.numOK = float64(v), !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
		}
	}
	h.tags[key] = tv
	if key == "Image ExifOffset" {
		if len(values) == 0 {
			*eo = exifOffset{empty: true}
		} else if v, ok := values[0].(int64); ok {
			*eo = exifOffset{off: v, ok: true}
		} else {
			*eo = exifOffset{}
		}
	}
	return nil
}

// ascii is _process_ascii_field: the text up to the first NUL, a str when it is UTF-8, else the raw bytes.
func (h *header) ascii(count, off int64) (string, []byte, bool) {
	if count == 0 {
		return "", nil, true
	}
	b := h.f.at(h.offset+off, count)
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	if utf8.Valid(b) {
		return string(b), nil, true
	}
	return "", b, false
}

// field is _process_field for the non-ASCII types.
func (h *header) field(count int64, ft int, tl, off int64) ([]any, error) {
	var values []any
	if count >= 1000 {
		return values, nil
	}
	signed := signedType(ft)
	for range count {
		switch ft {
		case tRatio, tSRatio:
			n, err := h.s2n(off, 4, signed)
			if err != nil {
				return nil, err
			}
			d, err := h.s2n(off+4, 4, signed)
			if err != nil {
				return nil, err
			}
			values = append(values, newRatio(n, d))
		case tFloat, tDouble:
			if h.offset+off < 0 {
				return nil, errExifread
			}
			b := h.f.at(h.offset+off, tl)
			if int64(len(b)) == tl {
				var bo binary.ByteOrder = binary.BigEndian
				if h.little {
					bo = binary.LittleEndian
				}
				if ft == tFloat {
					values = append(values, floatTuple(math.Float32frombits(bo.Uint32(b))))
				} else {
					values = append(values, floatTuple(math.Float64frombits(bo.Uint64(b))))
				}
			}
		default:
			v, err := h.s2n(off, int(tl), signed)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		off += tl
	}
	return values, nil
}

func pyRepr(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case ratio:
		return x.String()
	case floatTuple:
		return "(" + py.FloatRepr(float64(x)) + ",)"
	}
	return fmt.Sprint(v)
}

func pyListRepr(vs []any) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = pyRepr(v)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// exifreadTags returns the tags read() uses, keyed like exifread ("Image Model", "EXIF FNumber"...), or an error
// wherever exifread would have raised or returned {} — both of which sent read() to the Pillow fallback.
func exifreadTags(f *file) (map[string]*tagVal, error) {
	off, endian, err := determineType(f)
	if err != nil {
		return nil, err
	}
	if len(endian) == 0 {
		return nil, errExifread
	}
	h := &header{f: f, little: endian[0] == 'I', offset: off, tags: map[string]*tagVal{}}
	var eo exifOffset
	for ctr, ifd := range h.listIFD() {
		name := "Image"
		switch {
		case ctr == 1:
			name = "Thumbnail"
		case ctr > 1:
			name = fmt.Sprintf("IFD %d", ctr)
		}
		if err := h.dumpIFD(ifd, name, &eo); err != nil {
			return nil, err
		}
	}
	if h.tags["Image ExifOffset"] != nil {
		switch {
		case eo.empty:
			return nil, errExifread
		case eo.ok:
			if err := h.dumpIFD(eo.off, "EXIF", &eo); err != nil {
				return nil, err
			}
		}
	}
	if !h.any {
		return nil, errExifread
	}
	return h.tags, nil
}
