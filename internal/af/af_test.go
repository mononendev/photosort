package af

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
)

const repoRoot = "../.."

type pt struct{ x, y, w, h int }

type wordsOpt struct {
	mode              int
	size              [2]int
	inFocus, selected []int
	primary           *int
}

// words builds an AFInfo2 word array (unsigned, as the maker note stores it) from (x, y, w, h) points.
func words(points []pt, o wordsOpt) []int {
	if o.size == [2]int{} {
		o.size = [2]int{5184, 3456}
	}
	n := len(points)
	nw := (n + 15) / 16
	w := []int{0, o.mode, n, n, o.size[0], o.size[1], o.size[0], o.size[1]}
	for _, get := range []func(pt) int{
		func(p pt) int { return p.w }, func(p pt) int { return p.h },
		func(p pt) int { return p.x }, func(p pt) int { return p.y },
	} {
		for _, p := range points {
			w = append(w, get(p))
		}
	}
	for _, bits := range [][]int{o.inFocus, o.selected} {
		ws := make([]int, nw)
		for _, i := range bits {
			ws[i/16] |= 1 << (i % 16)
		}
		w = append(w, ws...)
	}
	if o.primary != nil {
		w = append(w, *o.primary)
	}
	for i := range w {
		w[i] &= 0xFFFF
	}
	return w
}

func intp(i int) *int { return &i }

func point(raw pj.Obj, i int) pj.Obj { return pj.A(raw, "points")[i].(pj.Obj) }

func box(af pj.Obj, i int) []any { return point(af, i)["box"].([]any) }

func TestParseSignedAndBits(t *testing.T) {
	raw := ParseAFInfo2(words([]pt{{0, 0, 100, 100}, {-1000, 800, 100, 100}},
		wordsOpt{mode: 2, inFocus: []int{1}, selected: []int{1}, primary: intp(1)}))
	if !reflect.DeepEqual(raw["af_size"], []any{5184, 3456}) {
		t.Fatalf("af_size = %v", raw["af_size"])
	}
	if point(raw, 1)["x"] != -1000 || point(raw, 1)["y"] != 800 {
		t.Fatalf("point 1 = %v", point(raw, 1))
	}
	if point(raw, 1)["in_focus"] != true || point(raw, 0)["in_focus"] != false {
		t.Fatal("in_focus bits")
	}
	if raw["primary_point"] != 1 {
		t.Fatalf("primary_point = %v", raw["primary_point"])
	}
	if ParseAFInfo2([]int{1, 2, 3}) != nil {
		t.Fatal("short array should be nil")
	}
}

func TestToFrameLandscapeYUp(t *testing.T) {
	raw := ParseAFInfo2(words([]pt{{-1000, 800, 100, 100}}, wordsOpt{mode: 2, selected: []int{0}}))
	out := ToFrame(raw, 5184, 3456, 1, true)
	// center 2592+(-1000)=1592, 1728-800=928 (y counts upward)
	if !reflect.DeepEqual(box(out, 0), []any{1542, 878, 1642, 978}) {
		t.Fatalf("box = %v", box(out, 0))
	}
	if !reflect.DeepEqual(out["active"], []any{0}) || out["active_from"] != "selected" || out["mode_name"] != "single-point" {
		t.Fatalf("out = %v", out)
	}
}

func TestToFramePortraitRotation(t *testing.T) {
	raw := ParseAFInfo2(words([]pt{{-1000, 800, 100, 50}}, wordsOpt{mode: 2, inFocus: []int{0}}))
	// Camera rotated 90 CW for display (orientation 6): upright frame is 3456 x 5184.
	b := box(ToFrame(raw, 3456, 5184, 6, true), 0)
	// sensor box (1542, 903, 1642, 953) -> x' = H - y, y' = x
	if !reflect.DeepEqual(b, []any{3456 - 953, 1542, 3456 - 903, 1642}) {
		t.Fatalf("orientation 6 box = %v", b)
	}
	b8 := box(ToFrame(raw, 3456, 5184, 8, true), 0)
	if !reflect.DeepEqual(b8, []any{903, 5184 - 1642, 953, 5184 - 1542}) {
		t.Fatalf("orientation 8 box = %v", b8)
	}
}

// The pick_primary tests live with local (which picks the primary person); what they rely on from here is
// PersonScore, checked on the same scenes.
func TestPickPrimaryFollowsAF(t *testing.T) {
	big := pj.Obj{"box": []any{0, 0, 2000, 3000}, "head": []any{800, 0, 1200, 400}, "torso": []any{0, 400, 2000, 1500}, "priority": 0.5}
	small := pj.Obj{"box": []any{3000, 1000, 3400, 2000}, "head": []any{3100, 1000, 3300, 1200},
		"torso": []any{3000, 1200, 3400, 1500}, "priority": 0.05}
	pts := pj.Obj{"points": []any{pj.Obj{"i": 0, "box": []any{3150, 1050, 3250, 1150}}}, "active": []any{0}}
	if s := PersonScore(pts, small, 0); s != 2.0 { // local.pick_primary: small["af_score"] == 2.0
		t.Fatalf("small score = %v", s)
	}
	if s := PersonScore(pts, big, 0); s != 0 {
		t.Fatalf("big score = %v", s)
	}
	// Point inside two overlapping heads: the one centered on it wins.
	a := pj.Obj{"box": []any{0, 0, 400, 800}, "head": []any{100, 0, 300, 200}, "priority": 0.5}
	b := pj.Obj{"box": []any{150, 0, 550, 800}, "head": []any{150, 0, 350, 200}, "priority": 0.4}
	p2 := pj.Obj{"points": []any{pj.Obj{"i": 0, "box": []any{240, 90, 260, 110}}}, "active": []any{0}}
	if sa, sb := PersonScore(p2, a, 0), PersonScore(p2, b, 0); sb <= sa {
		t.Fatalf("centered head should win: a=%v b=%v", sa, sb)
	}
	// AF on empty background: nobody scores.
	p3 := pj.Obj{"points": []any{pj.Obj{"i": 0, "box": []any{5000, 3300, 5100, 3400}}}, "active": []any{0}}
	if PersonScore(p3, small, 0) != 0 || PersonScore(p3, big, 0) != 0 {
		t.Fatal("background point scored")
	}
	if PersonScore(nil, small, 0) != 0 || PersonScore(pj.Obj{"points": []any{}, "active": []any{}}, small, 0) != 0 {
		t.Fatal("no AF should score 0")
	}
}

func TestPickPrimaryAFPointBesideAHead(t *testing.T) {
	// IMG_1055: a centre spot point just right of a spectator's head (box overlap only), rider bigger at the edge.
	rider := pj.Obj{"box": []any{3090, 3671, 3450, 4779}, "head": []any{3111, 3705, 3266, 3860},
		"torso": []any{3090, 3890, 3450, 4194}, "priority": 0.0129}
	spectator := pj.Obj{"box": []any{1464, 2354, 1696, 3124}, "head": []any{1502, 2359, 1630, 2487},
		"torso": []any{1464, 2514, 1696, 2766}, "priority": 0.009}
	pts := pj.Obj{"points": []any{pj.Obj{"i": 30, "box": []any{1644, 2506, 1812, 2678}}}, "active": []any{30}}
	// Without near the spectator's box overlap stays under min_score 0.5; with near=2 the head earns credit.
	if s := PersonScore(pts, spectator, 0); s >= 0.5 {
		t.Fatalf("spectator score without near = %v", s)
	}
	if s := PersonScore(pts, spectator, 2.0); s < 0.5 {
		t.Fatalf("spectator score with near = %v", s)
	}
	if s := PersonScore(pts, rider, 2.0); s != 0 {
		t.Fatalf("rider score = %v", s)
	}
	// Credit fades with distance: nothing at near point-widths away.
	far := pj.Obj{"box": []any{1000, 0, 1100, 100}, "head": []any{1000, 0, 1100, 100}}
	one := pj.Obj{"points": []any{pj.Obj{"i": 0, "box": []any{0, 0, 100, 100}}}, "active": []any{0}}
	if s := PersonScore(one, far, 2.0); s != 0.0 {
		t.Fatalf("far score = %v", s)
	}
}

func TestScoresNearerPersonOccludes(t *testing.T) {
	// IMG_1074 (boxes read off the overlay): a centre spot point between a rider's legs, just beside the head and
	// torso of a seated kid behind. Alone, the kid's near-credit beats the rider's loose body-box hit.
	rider := pj.Obj{"box": []any{918, 1042, 2587, 3069}, "head": []any{1697, 1098, 2201, 1602},
		"torso": []any{1434, 1417, 2106, 2089}}
	kid := pj.Obj{"box": []any{1355, 2313, 1702, 2890}, "head": []any{1490, 2324, 1658, 2481},
		"torso": []any{1434, 2453, 1686, 2677}}
	pts := pj.Obj{"points": []any{pj.Obj{"i": 0, "box": []any{1641, 2509, 1809, 2677}}}, "active": []any{0}}
	ppl := []pj.Obj{rider, kid}
	if s := Scores(pts, ppl, 2.0, 0); s[1] <= s[0] {
		t.Fatalf("without occlusion the kid should outscore the rider: %v", s)
	}
	s := Scores(pts, ppl, 2.0, 2.0)
	if s[1] != 0 || s[0] < 0.5 {
		t.Fatalf("the rider is in front, so the point is theirs: %v", s)
	}
	// A point squarely on the small one's head still counts: their face is what's visible there.
	onHead := pj.Obj{"points": []any{pj.Obj{"i": 0, "box": []any{1530, 2360, 1620, 2450}}}, "active": []any{0}}
	if s := Scores(onHead, ppl, 2.0, 2.0); s[1] <= s[0] {
		t.Fatalf("head hit behind should still win: %v", s)
	}
	// Similar sizes don't occlude.
	twin := pj.Obj{"box": []any{1300, 2200, 1800, 2900}}
	if s := Scores(pts, []pj.Obj{twin, kid}, 2.0, 2.0); s[1] == 0 {
		t.Fatalf("a similar-sized box shouldn't block: %v", s)
	}
	// IMG_1055: the bigger rider is off to the side, so the spectator keeps the point.
	spectator := pj.Obj{"box": []any{1464, 2354, 1696, 3124}, "head": []any{1502, 2359, 1630, 2487},
		"torso": []any{1464, 2514, 1696, 2766}}
	far := pj.Obj{"box": []any{3090, 3671, 3450, 4779}, "head": []any{3111, 3705, 3266, 3860}}
	p55 := pj.Obj{"points": []any{pj.Obj{"i": 30, "box": []any{1644, 2506, 1812, 2678}}}, "active": []any{30}}
	if s := Scores(p55, []pj.Obj{far, spectator}, 2.0, 2.0); s[1] != 0.943 {
		t.Fatalf("spectator score = %v", s)
	}
}

func TestPersonScoreValues(t *testing.T) {
	// Python: af.person_score for the IMG_1055 scene.
	spectator := pj.Obj{"box": []any{1464, 2354, 1696, 3124}, "head": []any{1502, 2359, 1630, 2487},
		"torso": []any{1464, 2514, 1696, 2766}}
	pts := pj.Obj{"points": []any{pj.Obj{"i": 30, "box": []any{1644, 2506, 1812, 2678}}}, "active": []any{30.0}}
	if s := PersonScore(pts, spectator, 0); s != 0.332 {
		t.Fatalf("score = %v", s)
	}
	if s := PersonScore(pts, spectator, 2.0); s != 0.943 {
		t.Fatalf("near score = %v", s)
	}
}

func writeFile(t *testing.T, name string, b []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadMissingIsNone(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 32)), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadWithNote(writeFile(t, "x.jpg", buf.Bytes()), 32, 32, true); got != nil {
		t.Fatalf("got %v", got)
	}
}

// tiff is a minimal little-endian TIFF: IFD0 (Orientation, ExifIFD) -> Exif IFD (MakerNote) -> maker note IFD
// (0x0026).
func tiff(words []int, orientation int) []byte {
	le := binary.LittleEndian
	b := append([]byte("II*\x00"), le.AppendUint32(nil, 8)...)
	const ifd0, exif, mn, arr = 8, 8 + 2 + 24 + 4, 8 + 2 + 24 + 4 + 2 + 12 + 4, 8 + 2 + 24 + 4 + 2 + 12 + 4 + 2 + 12 + 4
	entry := func(b []byte, tag, typ uint16, count, val uint32) []byte {
		b = le.AppendUint16(b, tag)
		b = le.AppendUint16(b, typ)
		b = le.AppendUint32(b, count)
		return le.AppendUint32(b, val)
	}
	b = le.AppendUint16(b, 2)
	b = entry(b, 0x0112, 3, 1, uint32(orientation))
	b = entry(b, 0x8769, 4, 1, exif)
	b = append(b, 0, 0, 0, 0)
	b = le.AppendUint16(b, 1)
	b = entry(b, 0x927C, 7, 0, mn)
	b = append(b, 0, 0, 0, 0)
	b = le.AppendUint16(b, 1)
	b = entry(b, 0x0026, 3, uint32(len(words)), arr)
	b = append(b, 0, 0, 0, 0)
	if len(b) != arr {
		panic("layout")
	}
	for _, w := range words {
		b = le.AppendUint16(b, uint16(w))
	}
	return b
}

func TestReadTIFFMakerNote(t *testing.T) {
	p := writeFile(t, "x.cr2", tiff(words([]pt{{0, 0, 171, 171}, {1174, 0, 171, 171}},
		wordsOpt{mode: 9, inFocus: []int{1}, selected: []int{1}}), 8))
	got, note := ReadWithNote(p, 3456, 5184, true)
	if got["mode_name"] != "spot" || !reflect.DeepEqual(got["active"], []any{1}) || note != "spot, 1 active" {
		t.Fatalf("got %v, %q", got, note)
	}
	// Orientation 8 (upright = sensor rotated 90 CCW): sensor-right lands in the upper half of the portrait frame.
	b := box(got, 0)
	cy := float64(b[1].(int)+b[3].(int)) / 2
	cx := float64(b[0].(int)+b[2].(int)) / 2
	if !(cy < 5184.0/2-1000) || !(cx-3456.0/2 < 2 && 3456.0/2-cx < 2) {
		t.Fatalf("box = %v", b)
	}
}

func TestManualFocusHasNoActivePoints(t *testing.T) {
	raw := ParseAFInfo2(words([]pt{{0, 0, 100, 100}, {500, 0, 100, 100}}, wordsOpt{mode: 0, selected: []int{0, 1}}))
	out := ToFrame(raw, 5184, 3456, 1, true)
	if len(pj.A(out, "active")) != 0 || len(pj.A(out, "points")) != 0 || out["mode_name"] != "manual focus" {
		t.Fatalf("out = %v", out)
	}
}

func TestPrimaryPointPaddingIgnored(t *testing.T) {
	// 1D X: the word after the bitmasks is 0 padding, not "point 0"
	raw := ParseAFInfo2(words([]pt{{0, 0, 100, 100}, {500, 0, 100, 100}},
		wordsOpt{mode: 2, inFocus: []int{1}, selected: []int{1}, primary: intp(0)}))
	if raw["primary_point"] != nil {
		t.Fatalf("primary_point = %v", raw["primary_point"])
	}
}

func TestReal1DXCR2IfPresent(t *testing.T) {
	p := "/Users/mononen/Programming/photosort/dev-data/cr2/IMG_1010.CR2"
	if _, err := os.Stat(filepath.Join(repoRoot, "dev-data/cr2/IMG_1010.CR2")); err == nil {
		p = filepath.Join(repoRoot, "dev-data/cr2/IMG_1010.CR2")
	} else if _, err := os.Stat(p); err != nil {
		t.Skip("local CR2 sample not present (dev-data/cr2 is gitignored)")
	}
	got, _ := ReadWithNote(p, 3456, 5184, true)
	if got["mode_name"] != "spot" || !reflect.DeepEqual(got["active"], []any{30}) || got["n_points"] != 61 {
		t.Fatalf("got %v", got)
	}
}

func TestRescoreOldRowsWithoutPriority(t *testing.T) {
	t.Skip("ported with rules/rescore")
}

func TestReadFailureNotes(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.cr2")
	if _, note := ReadWithNote(missing, 10, 10, true); note != "AF read failed: FileNotFoundError: [Errno 2] No such file or directory: '"+missing+"'" {
		t.Fatalf("note = %q", note)
	}
	if _, note := ReadWithNote(writeFile(t, "empty.jpg", nil), 10, 10, true); note != noAFNote() {
		t.Fatalf("note = %q", note)
	}
}

func noAFNote() string {
	if hasExiftool() {
		return "no AF info in file"
	}
	return "no Canon AF info (exiftool not installed)"
}

func resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(repoRoot, p)
}

// wantNote adjusts a Python note recorded without exiftool to this machine.
func wantNote(n string) string {
	if n == "no Canon AF info (exiftool not installed)" {
		return noAFNote()
	}
	return n
}

// TestGoldenPython compares against testdata/exif_golden.json, dumped from the Python af.read_with_note.
func TestGoldenPython(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "testdata/exif_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Entries []struct {
			Path   string `json:"path"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
			AF     pj.Obj `json:"af"`
			Note   string `json:"af_note"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	for _, e := range g.Entries {
		t.Run(filepath.Base(e.Path), func(t *testing.T) {
			p := resolve(e.Path)
			if _, err := os.Stat(p); err != nil {
				t.Skipf("%s not present", e.Path)
			}
			got, note := ReadWithNote(p, e.Width, e.Height, true)
			if !pj.Equal(got, e.AF) || note != wantNote(e.Note) {
				t.Errorf("ReadWithNote(%s)\n got  %s %q\n want %s %q", e.Path, pj.Dumps(got), note, pj.Dumps(e.AF), e.Note)
			}
		})
	}
}

// TestGoldenFixtures checks local.af / local.af_note in the pipeline golden fixtures.
func TestGoldenFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(repoRoot, "testdata/golden*/*.json"))
	if len(files) == 0 {
		t.Fatal("no golden fixtures")
	}
	for _, fx := range files {
		b, err := os.ReadFile(fx)
		if err != nil {
			t.Fatal(err)
		}
		var g struct {
			Path  string `json:"path"`
			Local struct {
				AF     pj.Obj `json:"af"`
				Note   string `json:"af_note"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"local"`
		}
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(fx), func(t *testing.T) {
			p := resolve(g.Path)
			if _, err := os.Stat(p); err != nil {
				t.Skipf("%s not present", g.Path)
			}
			got, note := ReadWithNote(p, g.Local.Width, g.Local.Height, true)
			if !pj.Equal(got, g.Local.AF) || note != wantNote(g.Local.Note) {
				t.Errorf("ReadWithNote(%s)\n got  %s %q\n want %s %q", g.Path, pj.Dumps(got), note,
					pj.Dumps(g.Local.AF), g.Local.Note)
			}
		})
	}
}
