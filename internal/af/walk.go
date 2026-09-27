package af

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mononendev/photosort/internal/pj"
)

// pyError is a Python exception as read_with_note printed it: "<TypeName>: <message>".
type pyError struct{ typ, msg string }

func (e *pyError) Error() string { return e.typ + ": " + e.msg }

// structErr is the struct.error a short read raised in the Python walker.
func structErr(format string, args ...any) error {
	return &pyError{"error", fmt.Sprintf(format, args...)}
}

// osError renders an open/read failure like Python's OSError subclasses ("[Errno 2] No such file or directory: 'p'").
func osError(err error, path string) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return &pyError{"OSError", err.Error()}
	}
	typ := "OSError"
	switch errno {
	case syscall.ENOENT:
		typ = "FileNotFoundError"
	case syscall.EACCES, syscall.EPERM:
		typ = "PermissionError"
	case syscall.EISDIR:
		typ = "IsADirectoryError"
	case syscall.ENOTDIR:
		typ = "NotADirectoryError"
	}
	msg := errno.Error()
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return &pyError{typ, fmt.Sprintf("[Errno %d] %s: %s", int(errno), msg, pyStrRepr(path))}
}

// pyStrRepr is repr(str) for the plain paths and tokens it is used on.
func pyStrRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "\t", `\t`, q, `\`+q)
	return q + r.Replace(s) + q
}

// readTIFF returns the AFInfo2/AFInfo3 words and the orientation, walked straight out of a TIFF-based file (CR2,
// JPEG APP1): IFD0 -> Exif IFD (0x8769) -> MakerNote (0x927C), whose offsets are relative to the TIFF header.
func readTIFF(path string) ([]int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 1, osError(err, path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 1, osError(err, path)
	}
	if st.IsDir() {
		return nil, 1, osError(syscall.EISDIR, path)
	}
	return walk(f, st.Size())
}

// walk reads per field: only a few KB of a 25 MB raw are touched, which matters on a network mount.
func walk(f io.ReaderAt, size int64) ([]int, int, error) {
	read := func(off, n int64) []byte {
		if off >= size {
			return nil
		}
		n = min(n, size-off)
		b := make([]byte, n)
		k, _ := f.ReadAt(b, off)
		return b[:k]
	}
	base := int64(0)
	head := read(0, 65536)
	if bytes.HasPrefix(head, []byte{0xFF, 0xD8}) { // JPEG: TIFF header follows "Exif\0\0" in APP1
		i := bytes.Index(head, []byte("Exif\x00\x00"))
		if i < 0 {
			return nil, 1, nil
		}
		base = int64(i + 6)
	}
	var bo binary.ByteOrder
	switch string(head[min(int(base), len(head)):min(int(base)+2, len(head))]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, 1, nil
	}
	u16 := func(o int64) (int, error) {
		b := read(base+o, 2)
		if len(b) != 2 {
			return 0, structErr("unpack requires a buffer of 2 bytes")
		}
		return int(bo.Uint16(b)), nil
	}
	u32 := func(o int64) (int64, error) {
		b := read(base+o, 4)
		if len(b) != 4 {
			return 0, structErr("unpack requires a buffer of 4 bytes")
		}
		return int64(bo.Uint32(b)), nil
	}
	type field struct {
		typ   int
		count int64
		voff  int64
	}
	entries := func(off int64) (map[int]field, error) {
		n, err := u16(off)
		if err != nil {
			return nil, err
		}
		raw := read(base+off+2, int64(12*n))
		out := map[int]field{}
		for i := range n {
			if len(raw) < 12*i+8 {
				return nil, structErr("unpack_from requires a buffer of at least %d bytes for unpacking 8 bytes at "+
					"offset %d (actual buffer size is %d)", 12*i+8, 12*i, len(raw))
			}
			e := raw[12*i:]
			out[int(bo.Uint16(e))] = field{int(bo.Uint16(e[2:])), int64(bo.Uint32(e[4:])), off + 10 + int64(12*i)}
		}
		return out, nil
	}
	at := func(fd field) (map[int]field, error) {
		o, err := u32(fd.voff)
		if err != nil {
			return nil, err
		}
		return entries(o)
	}

	first, err := u32(4)
	if err != nil {
		return nil, 1, err
	}
	ifd0, err := entries(first)
	if err != nil {
		return nil, 1, err
	}
	orientation := 1
	if fd, ok := ifd0[0x0112]; ok {
		if orientation, err = u16(fd.voff); err != nil {
			return nil, 1, err
		}
	}
	ex, ok := ifd0[0x8769]
	if !ok {
		return nil, orientation, nil
	}
	exif, err := at(ex)
	if err != nil {
		return nil, orientation, err
	}
	mnf, ok := exif[0x927C]
	if !ok {
		return nil, orientation, nil
	}
	mn, err := at(mnf)
	if err != nil {
		return nil, orientation, err
	}
	for _, tag := range []int{0x0026, 0x003C} {
		fd, ok := mn[tag]
		if !ok {
			continue
		}
		if (fd.typ != 3 && fd.typ != 8) || fd.count == 0 {
			continue
		}
		off := fd.voff
		if fd.count*2 > 4 {
			if off, err = u32(fd.voff); err != nil {
				return nil, orientation, err
			}
		}
		b := read(base+off, 2*fd.count)
		if int64(len(b)) != 2*fd.count {
			return nil, orientation, structErr("unpack requires a buffer of %d bytes", 2*fd.count)
		}
		words := make([]int, fd.count)
		for i := range words {
			words[i] = int(bo.Uint16(b[2*i:]))
		}
		return words, orientation, nil
	}
	return nil, orientation, nil
}

// pyStr is str(v) for a JSON value decoded with UseNumber.
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	}
	return fmt.Sprint(v)
}

// pyInt is int(s) for a str: optional sign, digits with single underscores between them, surrounding whitespace.
func pyInt(s string) (int, error) {
	t := strings.TrimSpace(s)
	body := strings.TrimLeft(t, "+-")
	ok := len(t)-len(body) <= 1 && body != "" && body[0] != '_' && body[len(body)-1] != '_' &&
		!strings.Contains(body, "__")
	if ok {
		for _, r := range body {
			if (r < '0' || r > '9') && r != '_' {
				ok = false
				break
			}
		}
	}
	if ok {
		if v, err := strconv.Atoi(strings.ReplaceAll(t, "_", "")); err == nil {
			return v, nil
		}
	}
	return 0, &pyError{"ValueError", "invalid literal for int() with base 10: " + pyStrRepr(s)}
}

// pyIntAny is int(v) for a JSON value: numbers truncate, strings parse, booleans are 0/1.
func pyIntAny(v any) (int, error) {
	switch x := v.(type) {
	case json.Number:
		if i, err := strconv.Atoi(x.String()); err == nil {
			return i, nil
		}
		f, err := x.Float64()
		if err != nil {
			return 0, &pyError{"ValueError", "invalid literal for int() with base 10: " + pyStrRepr(x.String())}
		}
		return int(f), nil
	case string:
		return pyInt(x)
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, &pyError{"TypeError", fmt.Sprintf("int() argument must be a string, a bytes-like object or a real number, not '%s'", pyType(v))}
}

func pyType(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	case json.Number:
		if _, err := strconv.Atoi(v.(json.Number).String()); err == nil {
			return "int"
		}
		return "float"
	}
	return "object"
}

// truthy is Python's bool() on a UseNumber-decoded value.
func truthy(v any) bool {
	if n, ok := v.(json.Number); ok {
		f, _ := n.Float64()
		return f != 0
	}
	return pj.Truthy(v)
}

// readExiftool asks exiftool for the AF layout (CR3 and other files the TIFF walk can't read). A missing exiftool,
// a timeout or unparsable output all mean "nothing found"; a malformed value inside the output is an error, as it
// was in Python.
func readExiftool(path string) (pj.Obj, int, error) {
	exe, err := exec.LookPath("exiftool")
	if err != nil {
		return nil, 1, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "-j", "-n", "-Orientation", "-AFAreaMode", "-NumAFPoints",
		"-ValidAFPoints", "-AFImageWidth", "-AFImageHeight", "-AFAreaWidths", "-AFAreaHeights", "-AFAreaXPositions",
		"-AFAreaYPositions", "-AFPointsInFocus", "-AFPointsSelected", "-PrimaryAFPoint", path).Output()
	if ctx.Err() != nil {
		return nil, 1, nil
	}
	_ = err // Python ignored the exit status and parsed whatever came out
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var all []any
	if dec.Decode(&all) != nil || len(bytes.TrimSpace(out[dec.InputOffset():])) > 0 || len(all) == 0 {
		return nil, 1, nil
	}
	d, ok := all[0].(map[string]any)
	if !ok {
		return nil, 1, &pyError{"AttributeError", fmt.Sprintf("'%s' object has no attribute 'get'", pyType(all[0]))}
	}
	orientation := 1
	if o := d["Orientation"]; truthy(o) {
		if orientation, err = pyIntAny(o); err != nil {
			return nil, 1, err
		}
	}
	arr := func(k string) ([]int, error) {
		v, ok := d[k]
		if !ok || v == nil || v == "" {
			return nil, nil
		}
		var out []int
		for _, tok := range strings.Fields(pyStr(v)) {
			i, err := pyInt(tok)
			if err != nil {
				return nil, err
			}
			out = append(out, i)
		}
		return out, nil
	}
	idx := func(k string) (map[int]bool, error) { // exiftool -n prints point lists as comma-separated indices
		v, ok := d[k]
		set := map[int]bool{}
		if !ok || v == nil || v == "" {
			return set, nil
		}
		for _, tok := range strings.Fields(strings.ReplaceAll(pyStr(v), ",", " ")) {
			i, err := pyInt(tok)
			if err != nil {
				return nil, err
			}
			set[i] = true
		}
		return set, nil
	}
	var cols [4][]int
	for i, k := range []string{"AFAreaWidths", "AFAreaHeights", "AFAreaXPositions", "AFAreaYPositions"} {
		if cols[i], err = arr(k); err != nil {
			return nil, orientation, err
		}
	}
	ws, hs, xs, ys := cols[0], cols[1], cols[2], cols[3]
	n := min(len(ws), len(hs), len(xs), len(ys))
	if n == 0 || !truthy(d["AFImageWidth"]) || !truthy(d["AFImageHeight"]) {
		return nil, orientation, nil
	}
	foc, err := idx("AFPointsInFocus")
	if err != nil {
		return nil, orientation, err
	}
	sel, err := idx("AFPointsSelected")
	if err != nil {
		return nil, orientation, err
	}
	pts := []any{}
	for i := range n {
		if ws[i] > 0 && hs[i] > 0 {
			pts = append(pts, pj.Obj{"i": i, "x": xs[i], "y": ys[i], "w": ws[i], "h": hs[i],
				"in_focus": foc[i], "selected": sel[i]})
		}
	}
	var prim any
	switch p := d["PrimaryAFPoint"].(type) {
	case json.Number, bool:
		v, _ := pyIntAny(p)
		prim = v
	}
	mode := -1
	if m := d["AFAreaMode"]; truthy(m) {
		if mode, err = pyIntAny(m); err != nil {
			return nil, orientation, err
		}
	}
	aw, err := pyIntAny(d["AFImageWidth"])
	if err != nil {
		return nil, orientation, err
	}
	ah, err := pyIntAny(d["AFImageHeight"])
	if err != nil {
		return nil, orientation, err
	}
	return pj.Obj{"mode": mode, "af_size": []any{aw, ah}, "points": pts, "primary_point": prim,
		"source": "exiftool"}, orientation, nil
}
