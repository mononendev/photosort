package exif

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"

	"github.com/mononendev/photosort/internal/py"
)

// The Python read() fell back to Pillow's getexif() (with pillow_heif registered) when exifread found nothing or
// raised. This reproduces that path: locate the Exif blob the way Pillow's format plugins do, then read IFD0 and the
// Exif IFD with ImageFileDirectory_v2's rules (unknown types skipped, a truncated value ends the directory, ASCII
// loses one trailing NUL and decodes as Latin-1).

// pillowUnit is ImageFileDirectory_v2._load_dispatch's unit size per field type.
var pillowUnit = map[int]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8, 13: 4, 16: 8, 17: 8, 18: 8}

type pilTag struct {
	typ  int
	data []byte
}

type pilDir struct {
	bo   binary.ByteOrder
	tags map[int]pilTag
	buf  []byte
}

// load is ImageFileDirectory_v2.load at off within buf.
func (d *pilDir) load(off int64) {
	d.tags = map[int]pilTag{}
	b := d.buf
	pos := off
	read := func(n int64) ([]byte, bool) {
		if pos < 0 || pos+n > int64(len(b)) {
			return nil, false
		}
		r := b[pos : pos+n]
		pos += n
		return r, true
	}
	h, ok := read(2)
	if !ok {
		return
	}
	n := int(d.bo.Uint16(h))
	for range n {
		e, ok := read(12)
		if !ok {
			return
		}
		tag, typ, count := int(d.bo.Uint16(e)), int(d.bo.Uint16(e[2:])), int64(d.bo.Uint32(e[4:]))
		unit, ok := pillowUnit[typ]
		if !ok {
			continue
		}
		size := count * int64(unit)
		var data []byte
		if size > 4 {
			o := int64(d.bo.Uint32(e[8:]))
			if o+size > int64(len(b)) {
				return // _safe_read: "Truncated File Read" ends the directory
			}
			data = b[o : o+size]
		} else {
			data = e[8 : 8+size]
		}
		if len(data) == 0 {
			continue
		}
		d.tags[tag] = pilTag{typ, data}
	}
}

// value decodes a tag the way Exif.__getitem__ hands it back: (str, number) as read() would see them.
func (d *pilDir) value(tag int) (*tagVal, bool) {
	t, ok := d.tags[tag]
	if !ok {
		return nil, false
	}
	tv := &tagVal{}
	switch t.typ {
	case 2:
		s := t.data
		if len(s) > 0 && s[len(s)-1] == 0 {
			s = s[:len(s)-1]
		}
		runes := make([]rune, len(s))
		for i, c := range s {
			runes[i] = rune(c)
		}
		tv.str = string(runes)
		tv.num, tv.numOK = pyFloat(tv.str)
	case 1, 7: // bytes
		tv.str = pyBytesRepr(t.data)
		ascii := true
		for _, c := range t.data {
			if c >= 0x80 {
				ascii = false
			}
		}
		if ascii {
			tv.num, tv.numOK = pyFloat(string(t.data))
		}
	default:
		n := len(t.data) / pillowUnit[t.typ]
		if n == 0 {
			return nil, false
		}
		v, s := d.number(t, 0)
		tv.num, tv.numOK, tv.str = v, !math.IsNaN(v) && !math.IsInf(v, 0), s
	}
	if tv.numOK && (math.IsNaN(tv.num) || math.IsInf(tv.num, 0)) {
		tv.numOK = false
	}
	return tv, true
}

// number is the i-th value of a numeric tag and its str().
func (d *pilDir) number(t pilTag, i int) (float64, string) {
	u := pillowUnit[t.typ]
	b := t.data[i*u:]
	switch t.typ {
	case 3:
		v := d.bo.Uint16(b)
		return float64(v), strconv.Itoa(int(v))
	case 4, 13:
		v := d.bo.Uint32(b)
		return float64(v), strconv.FormatUint(uint64(v), 10)
	case 6:
		v := int8(b[0])
		return float64(v), strconv.Itoa(int(v))
	case 8:
		v := int16(d.bo.Uint16(b))
		return float64(v), strconv.Itoa(int(v))
	case 9:
		v := int32(d.bo.Uint32(b))
		return float64(v), strconv.Itoa(int(v))
	case 5, 10:
		var n, dd float64
		if t.typ == 5 {
			n, dd = float64(d.bo.Uint32(b)), float64(d.bo.Uint32(b[4:]))
		} else {
			n, dd = float64(int32(d.bo.Uint32(b))), float64(int32(d.bo.Uint32(b[4:])))
		}
		v := math.NaN() // IFDRational with a zero denominator
		if dd != 0 {
			v = n / dd
		}
		return v, py.FloatRepr(v)
	case 11:
		v := float64(math.Float32frombits(d.bo.Uint32(b)))
		return v, py.FloatRepr(v)
	case 12:
		v := math.Float64frombits(d.bo.Uint64(b))
		return v, py.FloatRepr(v)
	case 16, 18:
		v := d.bo.Uint64(b)
		return float64(v), strconv.FormatUint(v, 10)
	case 17:
		v := int64(d.bo.Uint64(b))
		return float64(v), strconv.FormatInt(v, 10)
	}
	return 0, ""
}

// pillowExif is Exif.load(blob) + get_ifd(0x8769): the tags read() uses, keyed like exifread.
func pillowExif(blob []byte, tiffFile bool) map[string]*tagVal {
	for bytes.HasPrefix(blob, []byte("Exif\x00\x00")) && !tiffFile {
		blob = blob[6:]
	}
	if len(blob) < 8 {
		return nil
	}
	var bo binary.ByteOrder
	switch string(blob[:4]) {
	case "II*\x00", "II\x00*":
		bo = binary.LittleEndian
	case "MM\x00*", "MM*\x00":
		bo = binary.BigEndian
	default: // not a TIFF header (BigTIFF inside an Exif blob isn't readable either)
		return nil
	}
	d := &pilDir{bo: bo, buf: blob}
	d.load(int64(bo.Uint32(blob[4:8])))
	out := map[string]*tagVal{}
	for tag, key := range map[int]string{0x0110: "Image Model", 0x0132: "Image DateTime"} {
		if tv, ok := d.value(tag); ok {
			out[key] = tv
		}
	}
	t, ok := d.tags[0x8769]
	if !ok {
		return out
	}
	var off int64
	switch t.typ {
	case 3, 4, 8, 9, 13, 16, 17, 18, 6:
		v, _ := d.number(t, 0)
		off = int64(v)
	default: // a non-integer offset: seek() raises TypeError, which get_ifd swallows
		return out
	}
	if off < 0 {
		return nil // ValueError on seek escapes get_ifd, and _pillow_tags returns {}
	}
	ex := &pilDir{bo: bo, buf: blob}
	ex.load(off)
	for tag, key := range map[int]string{0x829D: "EXIF FNumber", 0x829A: "EXIF ExposureTime",
		0x8827: "EXIF ISOSpeedRatings", 0x920A: "EXIF FocalLength", 0xA405: "EXIF FocalLengthIn35mmFilm",
		0xA434: "EXIF LensModel", 0x9003: "EXIF DateTimeOriginal"} {
		if tv, ok := ex.value(tag); ok {
			out[key] = tv
		}
	}
	return out
}

// pillowTags is _pillow_tags: the Exif blob of any format Pillow (plus pillow_heif) opens, read as above.
func pillowTags(f *file) map[string]*tagVal {
	head := f.at(0, 16)
	switch {
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return pillowExif(jpegApp1(f), false)
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return pillowExif(pngExif(f), false)
	case string(slice(head, 0, 4)) == "RIFF" && string(slice(head, 8, 12)) == "WEBP":
		return pillowExif(webpExif(f), false)
	case isOneOf(slice(head, 0, 4), "II*\x00", "MM\x00*", "II\x00*", "MM*\x00"):
		return pillowExif(f.at(0, f.size), true)
	case string(slice(head, 4, 8)) == "ftyp" && isOneOf(slice(head, 8, 12), "heic", "heix", "heim", "heis", "hevc",
		"hevx", "hevm", "hevs", "mif1", "msf1", "avif", "avis"):
		return pillowExif(heifExif(f), false)
	}
	return nil
}

// jpegApp1 is the first APP1 segment carrying "Exif\0\0", walking markers up to the start of scan.
func jpegApp1(f *file) []byte {
	pos := int64(2)
	for pos < f.size {
		m := f.at(pos, 2)
		if len(m) < 2 || m[0] != 0xFF {
			return nil
		}
		if m[1] == 0xFF { // fill byte
			pos++
			continue
		}
		marker := m[1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			pos += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			return nil
		}
		l := f.at(pos+2, 2)
		if len(l) < 2 {
			return nil
		}
		n := int64(binary.BigEndian.Uint16(l))
		if marker == 0xE1 && n >= 2 {
			s := f.at(pos+4, n-2)
			if bytes.HasPrefix(s, []byte("Exif\x00\x00")) {
				return s
			}
		}
		pos += 2 + n
	}
	return nil
}

func pngExif(f *file) []byte {
	for pos := int64(8); pos+8 <= f.size; {
		h := f.at(pos, 8)
		n := int64(binary.BigEndian.Uint32(h))
		if string(h[4:]) == "eXIf" {
			return f.at(pos+8, n)
		}
		if string(h[4:]) == "IEND" {
			return nil
		}
		pos += 12 + n
	}
	return nil
}

func webpExif(f *file) []byte {
	for pos := int64(12); pos+8 <= f.size; {
		h := f.at(pos, 8)
		n := int64(binary.LittleEndian.Uint32(h[4:]))
		if string(h[:4]) == "EXIF" {
			return f.at(pos+8, n)
		}
		pos += 8 + n + n&1
	}
	return nil
}

// heifExif is the Exif item's payload as pillow_heif hands it to Pillow: the item read through iloc (base offset,
// every extent, idat-stored items), minus the leading 4-byte offset to the TIFF header.
func heifExif(f *file) []byte {
	meta, ok := findBox(f, 0, f.size, "meta")
	if !ok {
		return nil
	}
	start, end := meta[0]+4, meta[1] // FullBox header
	iinf, ok1 := findBox(f, start, end, "iinf")
	iloc, ok2 := findBox(f, start, end, "iloc")
	if !ok1 || !ok2 {
		return nil
	}
	id, ok := exifItemID(f, iinf)
	if !ok {
		return nil
	}
	var idat []byte
	if b, ok := findBox(f, start, end, "idat"); ok {
		idat = f.at(b[0], b[1]-b[0])
	}
	data, ok := readItem(f, iloc, id, idat)
	if !ok || len(data) < 4 {
		return nil
	}
	skip := int(binary.BigEndian.Uint32(data)) + 4
	if len(data)-skip <= 4 {
		skip = 4
	} else if skip >= 6 && string(data[skip-6:skip]) == "Exif\x00\x00" {
		skip -= 6
	}
	return data[skip:]
}

// findBox returns the payload range [start, end) of the first box of that type between from and to.
func findBox(f *file, from, to int64, kind string) ([2]int64, bool) {
	for pos := from; pos+8 <= to; {
		h := f.at(pos, 8)
		if len(h) < 8 {
			break
		}
		size, hdr := int64(binary.BigEndian.Uint32(h)), int64(8)
		switch size {
		case 0:
			size = to - pos
		case 1:
			b := f.at(pos+8, 8)
			if len(b) < 8 {
				return [2]int64{}, false
			}
			size, hdr = int64(binary.BigEndian.Uint64(b)), 16
		}
		if size < hdr {
			break
		}
		if string(h[4:8]) == kind {
			return [2]int64{pos + hdr, min(pos+size, to)}, true
		}
		pos += size
	}
	return [2]int64{}, false
}

// bits reads big-endian fields out of a box payload.
type bits struct {
	b   []byte
	pos int
	bad bool
}

func (r *bits) n(size int) uint64 {
	if size == 0 {
		return 0
	}
	if r.pos+size > len(r.b) || size > 8 {
		r.bad = true
		return 0
	}
	var v uint64
	for _, x := range r.b[r.pos : r.pos+size] {
		v = v<<8 | uint64(x)
	}
	r.pos += size
	return v
}

func exifItemID(f *file, iinf [2]int64) (uint64, bool) {
	r := &bits{b: f.at(iinf[0], iinf[1]-iinf[0])}
	version := r.n(1)
	r.n(3)
	count := r.n(2)
	if version != 0 {
		count = count<<16 | r.n(2)
	}
	pos := int64(r.pos) + iinf[0]
	for range count {
		b, ok := findBox(f, pos, iinf[1], "infe")
		if !ok {
			return 0, false
		}
		e := &bits{b: f.at(b[0], b[1]-b[0])}
		v := e.n(1)
		e.n(3)
		if v >= 2 {
			var id uint64
			if v == 2 {
				id = e.n(2)
			} else {
				id = e.n(4)
			}
			e.n(2)
			if e.pos+4 <= len(e.b) && string(e.b[e.pos:e.pos+4]) == "Exif" && !e.bad {
				return id, true
			}
		}
		pos = b[1]
	}
	return 0, false
}

func readItem(f *file, iloc [2]int64, id uint64, idat []byte) ([]byte, bool) {
	r := &bits{b: f.at(iloc[0], iloc[1]-iloc[0])}
	version := r.n(1)
	r.n(3)
	s := r.n(1)
	offSize, lenSize := int(s>>4), int(s&15)
	s = r.n(1)
	baseSize, idxSize := int(s>>4), int(s&15)
	if version != 1 && version != 2 {
		idxSize = 0
	}
	var count uint64
	if version < 2 {
		count = r.n(2)
	} else {
		count = r.n(4)
	}
	for range count {
		var item uint64
		if version < 2 {
			item = r.n(2)
		} else {
			item = r.n(4)
		}
		method := uint64(0)
		if version == 1 || version == 2 {
			method = r.n(2) & 15
		}
		r.n(2)
		base := r.n(baseSize)
		n := r.n(2)
		var out []byte
		for range n {
			r.n(idxSize)
			off, ln := r.n(offSize), r.n(lenSize)
			if item != id {
				continue
			}
			switch method {
			case 0:
				out = append(out, f.at(int64(base+off), int64(ln))...)
			case 1:
				out = append(out, slice(idat, int(base+off), int(base+off+ln))...)
			}
		}
		if r.bad {
			return nil, false
		}
		if item == id {
			return out, true
		}
	}
	return nil, false
}
