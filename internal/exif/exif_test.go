package exif

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

const repoRoot = "../.."

// tiffEntry is one IFD entry; payloads over 4 bytes go to a data area after the IFDs.
type tiffEntry struct {
	tag, typ uint16
	count    uint32
	payload  []byte
}

// buildTIFF writes a little-endian TIFF: IFD0 (plus an ExifIFD pointer) -> Exif IFD, as Pillow's Exif.tobytes does.
func buildTIFF(ifd0, exifIFD []tiffEntry) []byte {
	le := binary.LittleEndian
	size := func(n int) int { return 2 + 12*n + 4 }
	ifd0 = append(ifd0, tiffEntry{0x8769, 4, 1, nil})
	o0, oe := 8, 8+size(len(ifd0))
	dataAt := oe + size(len(exifIFD))
	var data []byte
	build := func(es []tiffEntry) []byte {
		b := le.AppendUint16(nil, uint16(len(es)))
		for _, e := range es {
			b = le.AppendUint16(b, e.tag)
			b = le.AppendUint16(b, e.typ)
			b = le.AppendUint32(b, e.count)
			switch {
			case e.tag == 0x8769:
				b = le.AppendUint32(b, uint32(oe))
			case len(e.payload) <= 4:
				b = append(b, append(e.payload, make([]byte, 4-len(e.payload))...)...)
			default:
				b = le.AppendUint32(b, uint32(dataAt+len(data)))
				data = append(data, e.payload...)
			}
		}
		return append(b, 0, 0, 0, 0)
	}
	out := append([]byte("II*\x00"), le.AppendUint32(nil, uint32(o0))...)
	out = append(out, build(ifd0)...)
	out = append(out, build(exifIFD)...)
	return append(out, data...)
}

func ascii(tag uint16, s string) tiffEntry {
	return tiffEntry{tag, 2, uint32(len(s) + 1), append([]byte(s), 0)}
}

func rational(tag uint16, num, den uint32) tiffEntry {
	return tiffEntry{tag, 5, 1, binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, num), den)}
}

func short(tag, v uint16) tiffEntry {
	return tiffEntry{tag, 3, 1, binary.LittleEndian.AppendUint16(nil, v)}
}

// plainJPEG is a small JPEG with no metadata.
func plainJPEG(t *testing.T, w, h int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// exifJPEG is a JPEG as Pillow writes it with exif=: the Exif APP1 right after SOI. Pillow stores 1.4 as 7/5 and
// 1/30 as 1/30.
func exifJPEG(t *testing.T, fNum [2]uint32, shutter [2]uint32, focal uint32, iso uint16) string {
	tiff := buildTIFF([]tiffEntry{ascii(0x0110, "Canon EOS R6")}, []tiffEntry{
		rational(0x829D, fNum[0], fNum[1]), rational(0x829A, shutter[0], shutter[1]),
		rational(0x920A, focal, 1), short(0x8827, iso),
	})
	app1 := append([]byte{0xFF, 0xE1}, binary.BigEndian.AppendUint16(nil, uint16(len(tiff)+8))...)
	app1 = append(append(app1, "Exif\x00\x00"...), tiff...)
	img := plainJPEG(t, 64, 64)
	out := append(append(append([]byte{}, img[:2]...), app1...), img[2:]...)
	p := filepath.Join(t.TempDir(), "a.jpg")
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadJPEGExif(t *testing.T) {
	d := Read(exifJPEG(t, [2]uint32{7, 5}, [2]uint32{1, 30}, 85, 800))
	if d["camera"] != "Canon EOS R6" {
		t.Fatalf("camera = %v", d["camera"])
	}
	if math.Abs(pj.F(d["f_number"])-1.4) >= 1e-3 || math.Abs(pj.F(d["shutter_s"])-1.0/30) >= 1e-4 {
		t.Fatalf("f/shutter = %v %v", d["f_number"], d["shutter_s"])
	}
	if pj.F(d["focal_mm"]) != 85 || pj.F(d["iso"]) != 800 {
		t.Fatalf("focal/iso = %v %v", d["focal_mm"], d["iso"])
	}
}

func TestReadMissingIsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "b.jpg")
	if err := os.WriteFile(p, plainJPEG(t, 32, 32), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := Read(p); len(d) != 0 {
		t.Fatalf("Read = %v", d)
	}
	if s := Prior(pj.Obj{}, nil)["summary"]; s != nil {
		t.Fatalf("summary = %v", s)
	}
	if d := Read(filepath.Join(t.TempDir(), "missing.jpg")); len(d) != 0 {
		t.Fatalf("Read(missing) = %v", d)
	}
}

func TestPriorRules(t *testing.T) {
	slow := Prior(pj.Obj{"f_number": 1.4, "shutter_s": 1.0 / 30, "focal_mm": 85}, nil)
	if slow["dof_risk"] != "high" || slow["motion_risk"] != "high" || pj.F(slow["shake_stops"]) <= 1 {
		t.Fatalf("slow = %v", slow)
	}
	fast := Prior(pj.Obj{"f_number": 2.8, "shutter_s": 1.0 / 2000, "focal_35mm": 200}, nil)
	if fast["dof_risk"] != "high" || fast["motion_risk"] != "low" { // 71mm pupil: thin plane even at f/2.8
		t.Fatalf("fast = %v", fast)
	}
	if r := Prior(pj.Obj{"f_number": 5.6, "focal_35mm": 24}, nil)["dof_risk"]; r != "low" {
		t.Fatalf("f/5.6 24mm dof_risk = %v", r)
	}
	if r := Prior(pj.Obj{"f_number": 2.2, "focal_35mm": 135}, nil)["dof_risk"]; r != "high" {
		t.Fatalf("f/2.2 135mm dof_risk = %v", r)
	}
	// crop factor turns 50mm into 80mm-equivalent
	if s := Prior(pj.Obj{"shutter_s": 1.0 / 80, "focal_mm": 50}, pj.Obj{"crop_factor": 1.6})["shake_stops"]; s != 0.0 {
		t.Fatalf("shake_stops = %v", s)
	}
}

func TestPriorSummary(t *testing.T) {
	// Values the Python produced for the same input.
	got := Prior(pj.Obj{"camera": "Canon EOS-1D X", "f_number": 2.2, "shutter_s": 0.0005, "iso": 1000.0,
		"focal_mm": 135.0}, nil)
	want := pj.Obj{"dof_risk": "high", "motion_risk": "low", "shake_stops": -3.89, "pupil_mm": 61.4,
		"summary": "Canon EOS-1D X · 135mm · f/2.2 · 1/2000 s · ISO 1000. very shallow depth of field: only the " +
			"focus plane can be sharp, judge the head/eyes; fast shutter: motion blur unlikely."}
	if !pj.Equal(got, want) {
		t.Fatalf("Prior = %s\nwant    %s", pj.Dumps(got), pj.Dumps(want))
	}
	if s := FmtShutter(2); s != "2 s" {
		t.Fatalf("FmtShutter(2) = %q", s)
	}
	if s := FmtShutter(0); s != "?" {
		t.Fatalf("FmtShutter(0) = %q", s)
	}
}

func TestNoisePriorMeasuredWinsOverISO(t *testing.T) {
	cfg := pj.Obj{"medium_sigma": 2.0, "high_sigma": 3.5, "noisy_iso": 1600, "high_iso": 6400}
	f := func(v float64) *float64 { return &v }
	// ISO 1600 lifted 2 stops is ISO 6400's noise
	lifted := NoisePrior(pj.Obj{"iso": 1600}, 2.0, nil, cfg)
	if lifted["eff_iso"] != 6400 || lifted["risk"] != "high" || lifted["by"] != "iso" {
		t.Fatalf("lifted = %v", lifted)
	}
	if !strings.Contains(pj.Str(lifted["summary"]), "about ISO 6400") {
		t.Fatalf("summary = %v", lifted["summary"])
	}
	if r := NoisePrior(pj.Obj{"iso": 1600}, 0, nil, cfg)["risk"]; r != "medium" {
		t.Fatalf("iso 1600 risk = %v", r)
	}
	if s := NoisePrior(pj.Obj{"iso": 400}, 0, nil, cfg)["summary"]; s != nil {
		t.Fatalf("iso 400 summary = %v", s)
	}
	// a clean measurement overrides a scary ISO (the camera's noise reduction did its job), and vice versa
	if r := NoisePrior(pj.Obj{"iso": 12800}, 0, f(1.2), cfg)["risk"]; r != "low" {
		t.Fatalf("measured clean risk = %v", r)
	}
	if r := NoisePrior(pj.Obj{"iso": 200}, 0, f(4.0), cfg)["risk"]; r != "high" {
		t.Fatalf("measured noisy risk = %v", r)
	}
	if b := NoisePrior(pj.Obj{}, 0, nil, cfg)["by"]; b != nil {
		t.Fatalf("no iso by = %v", b)
	}
	want := "High noise (ISO 1600 lifted +2 EV, about ISO 6400): don't read grain as detail, or noise-reduction " +
		"smear as missed focus."
	if lifted["summary"] != want {
		t.Fatalf("summary = %q", lifted["summary"])
	}
	both := NoisePrior(pj.Obj{"iso": 200}, 0, f(4.0), cfg)["summary"]
	if both != "High noise (ISO 200; measured noise 4.0 levels): don't read grain as detail, or noise-reduction smear as missed focus." {
		t.Fatalf("summary = %q", both)
	}
}

func TestSlowShutterDemotesOnlyBorderlineTier3(t *testing.T) {
	t.Skip("ported with local (local_tier)")
}

func TestPyFormatting(t *testing.T) {
	for x, want := range map[float64]string{135: "135", 1.4: "1.4", 0.0005: "0.0005", 1e-05: "1e-05",
		1234567: "1.23457e+06", 100000: "100000", 2.25: "2.25"} {
		if got := pyG(x); got != want {
			t.Errorf("pyG(%v) = %q, want %q", x, got, want)
		}
	}
	for x, want := range map[float64]string{1.5: "1.5", 1234567: "1234567.0", 1e16: "1e+16", 1.5e-05: "1.5e-05",
		0.0001: "0.0001", -2: "-2.0", 0.1: "0.1"} {
		if got := py.FloatRepr(x); got != want {
			t.Errorf("py.FloatRepr(%v) = %q, want %q", x, got, want)
		}
	}
	if got := pyBytesRepr([]byte("Lens \xff\xfe'")); got != `b"Lens \xff\xfe'"` {
		t.Errorf("pyBytesRepr = %s", got)
	}
}

type goldenEntry struct {
	Path string `json:"path"`
	Exif pj.Obj `json:"exif"`
}

func resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(repoRoot, p)
}

// TestGoldenPython compares against testdata/exif_golden.json, dumped from the Python exif.read for the sample
// images, the local CR2/HEIC samples (skipped when absent) and the synthetic files in testdata/exif.
func TestGoldenPython(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "testdata/exif_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Entries []goldenEntry `json:"entries"`
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
			if got := Read(p); !pj.Equal(got, e.Exif) {
				t.Errorf("Read(%s)\n got  %s\n want %s", e.Path, pj.Dumps(got), pj.Dumps(e.Exif))
			}
		})
	}
}

// TestGoldenFixtures checks local.exif in the pipeline golden fixtures.
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
				Exif pj.Obj `json:"exif"`
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
			want := g.Local.Exif
			if want == nil {
				want = pj.Obj{}
			}
			if got := Read(p); !pj.Equal(got, want) {
				t.Errorf("Read(%s)\n got  %s\n want %s", g.Path, pj.Dumps(got), pj.Dumps(want))
			}
		})
	}
}
