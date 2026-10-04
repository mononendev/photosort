// Package af reads where the camera was asked to focus, from the maker notes.
//
// The AF points say what the photographer (or the camera's auto selection) meant to be the subject, independent of
// whether focus landed. The local stage uses them to pick the primary person, and the detail view draws them over
// the frame.
//
// Canon stores the AF layout in MakerNote tag 0x0026 (AFInfo2; 0x003C AFInfo3 on newer bodies) as int16 words, the
// same layout ExifTool documents:
//
//	0 size (bytes), 1 AFAreaMode, 2 NumAFPoints (N), 3 ValidAFPoints,
//	4-5 CanonImageWidth/Height, 6-7 AFImageWidth/Height,
//	then N widths, N heights, N x centers, N y centers,
//	then ceil(N/16) words of AFPointsInFocus bits, ceil(N/16) words of AFPointsSelected bits,
//	then PrimaryAFPoint on some bodies.
//
// Point centers are relative to the image center in AFImageWidth/Height units, x to the right, y upward
// (Cartesian). They are in sensor orientation, so they are rotated by the EXIF orientation to match the upright
// frame the rest of the pipeline sees.
//
// The maker note is read by walking the TIFF IFDs directly. Files that aren't TIFF-based (CR3) fall back to
// exiftool when installed.
package af

import (
	"fmt"
	"math"
	"os/exec"

	"github.com/mononendev/photosort/internal/pj"
)

// AreaModes names Canon's AFAreaMode values.
var AreaModes = map[int]string{
	0: "manual focus", 1: "AF point expansion (surround)", 2: "single-point", 4: "auto (multi-point)",
	5: "face detect", 6: "face + tracking", 7: "zone", 8: "AF point expansion (4 point)", 9: "spot",
	10: "AF point expansion (8 point)", 11: "flexizone multi (49 point)", 12: "flexizone multi (9 point)",
	13: "flexizone single", 14: "large zone", 16: "large zone (vertical)", 17: "large zone (horizontal)",
	19: "flexible zone 1", 20: "flexible zone 2", 21: "flexible zone 3", 22: "whole area", 23: "whole area (tracking)",
}

// UserPlaced are the modes where the photographer placed the point(s); in the others the camera chose.
var UserPlaced = map[int]bool{1: true, 2: true, 8: true, 9: true, 10: true, 13: true}

func s16(v int) int {
	v &= 0xFFFF
	if v >= 0x8000 {
		return v - 0x10000
	}
	return v
}

func bitsOf(words []int, n int) []bool {
	out := make([]bool, n)
	for i := range n {
		if i/16 < len(words) {
			out[i] = (words[i/16]>>(i%16))&1 == 1
		}
	}
	return out
}

// ParseAFInfo2 decodes the raw AFInfo2/AFInfo3 word array into sensor-frame points; nil if malformed.
//
// The result has "mode", "af_size" [w, h], "points" (each {"i", "x", "y", "w", "h", "in_focus", "selected"}) and
// "primary_point" (nil when absent).
func ParseAFInfo2(words []int) pj.Obj {
	w := make([]int, len(words))
	for i, v := range words {
		w[i] = s16(v)
	}
	if len(w) < 8 {
		return nil
	}
	n := w[2]
	nw := (n + 15) / 16
	if n <= 0 || len(w) < 8+4*n+nw {
		return nil
	}
	aw, ah := w[6]&0xFFFF, w[7]&0xFFFF
	if aw == 0 || ah == 0 {
		return nil
	}
	o := 8
	widths, heights := w[o:o+n], w[o+n:o+2*n]
	xs, ys := w[o+2*n:o+3*n], w[o+3*n:o+4*n]
	o += 4 * n
	inFocus := bitsOf(w[o:o+nw], n)
	o += nw
	selected := make([]bool, n)
	if len(w) >= o+nw {
		selected = bitsOf(w[o:o+nw], n)
	}
	o += nw
	var primary any
	if len(w) > o && w[o] >= 0 && w[o] < n {
		primary = w[o]
		if !inFocus[w[o]] && !selected[w[o]] {
			primary = nil // the 1D X has padding here, not a point index
		}
	}
	valid := n
	if w[3] > 0 && w[3] <= n {
		valid = w[3]
	}
	pts := []any{}
	for i := range valid {
		if widths[i] <= 0 || heights[i] <= 0 {
			continue
		}
		pts = append(pts, pj.Obj{"i": i, "x": xs[i], "y": ys[i], "w": widths[i], "h": heights[i],
			"in_focus": inFocus[i], "selected": selected[i]})
	}
	return pj.Obj{"mode": w[1], "af_size": []any{aw, ah}, "points": pts, "primary_point": primary}
}

// orient rotates a sensor-frame box (sensor W x H) into the upright frame.
func orient(b [4]float64, W, H float64, orientation int) [4]float64 {
	x0, y0, x1, y1 := b[0], b[1], b[2], b[3]
	switch orientation {
	case 3:
		return [4]float64{W - x1, H - y1, W - x0, H - y0}
	case 6: // rotate 90 CW
		return [4]float64{H - y1, x0, H - y0, x1}
	case 8: // rotate 90 CCW
		return [4]float64{y0, W - x1, y1, W - x0}
	}
	return b
}

// ToFrame places decoded points on the upright W x H frame as pixel boxes.
func ToFrame(raw pj.Obj, W, H int, orientation int, yUp bool) pj.Obj {
	sw, sh := float64(W), float64(H) // sensor-orientation size of this frame
	if orientation == 6 || orientation == 8 {
		sw, sh = sh, sw
	}
	size := pj.A(raw, "af_size")
	kx, ky := sw/pj.F(size[0]), sh/pj.F(size[1])
	var pts []pj.Obj
	for _, pv := range pj.A(raw, "points") {
		p := pv.(pj.Obj)
		y := pj.F(p["y"])
		if yUp {
			y = -y
		}
		cx := sw/2 + pj.F(p["x"])*kx
		cy := sh/2 + y*ky
		hw, hh := pj.F(p["w"])*kx/2, pj.F(p["h"])*ky/2
		b := orient([4]float64{cx - hw, cy - hh, cx + hw, cy + hh}, sw, sh, orientation)
		box := make([]any, 4)
		for i, v := range b {
			box[i] = pj.RoundInt(v)
		}
		pts = append(pts, pj.Obj{"i": p["i"], "box": box, "in_focus": p["in_focus"], "selected": p["selected"]})
	}
	mode := pj.Int(raw["mode"])
	var active []pj.Obj
	if mode != 0 { // manual focus: the body flags every point as selected, which says nothing about intent
		for _, p := range pts {
			if p["in_focus"] == true {
				active = append(active, p)
			}
		}
		if len(active) == 0 {
			for _, p := range pts {
				if p["selected"] == true {
					active = append(active, p)
				}
			}
		}
	}
	// Only the points worth drawing: every one on a 61-point body is noise on the overlay.
	shown := []any{}
	anyInFocus := false
	for _, p := range pts {
		if p["in_focus"] == true {
			anyInFocus = true
		}
		if mode != 0 && (p["in_focus"] == true || p["selected"] == true) {
			shown = append(shown, p)
		}
	}
	activeIdx := []any{}
	for _, p := range active {
		activeIdx = append(activeIdx, p["i"])
	}
	var activeFrom any
	switch {
	case anyInFocus:
		activeFrom = "in_focus"
	case len(active) > 0:
		activeFrom = "selected"
	}
	source, ok := raw["source"]
	if !ok {
		source = "canon"
	}
	name, ok := AreaModes[mode]
	if !ok {
		name = fmt.Sprintf("mode %d", mode)
	}
	return pj.Obj{
		"source": source, "mode": mode, "mode_name": name, "user_placed": UserPlaced[mode],
		"n_points": len(pts), "primary_point": raw["primary_point"],
		"points": shown, "active": activeIdx, "active_from": activeFrom,
	}
}

func hasExiftool() bool {
	_, err := exec.LookPath("exiftool")
	return err == nil
}

// ReadWithNote returns the AF points on the upright W x H frame (or nil) and a short note saying what was found.
// Never fails: errors come back as the note, worded as the Python did ("AF read failed: <Type>: <message>").
func ReadWithNote(path string, W, H int, yUp bool) (pj.Obj, string) {
	words, orientation, err := readTIFF(path)
	if err != nil {
		return nil, "AF read failed: " + err.Error()
	}
	var raw pj.Obj
	if words != nil {
		raw = ParseAFInfo2(words)
	}
	switch {
	case raw != nil:
		raw["source"] = "canon_afinfo2"
	case len(words) > 0:
		return nil, fmt.Sprintf("AF info present but not decodable (%d words)", len(words))
	default:
		raw, orientation, err = readExiftool(path)
		if err != nil {
			return nil, "AF read failed: " + err.Error()
		}
	}
	if raw == nil || len(pj.A(raw, "points")) == 0 {
		if hasExiftool() {
			return nil, "no AF info in file"
		}
		return nil, "no Canon AF info (exiftool not installed)"
	}
	af := ToFrame(raw, W, H, orientation, yUp)
	return af, fmt.Sprintf("%s, %d active", af["mode_name"], len(pj.A(af, "active")))
}

// overlap is the fraction of box a that lies inside box b.
func overlap(a, b []float64) float64 {
	ix := math.Max(0, math.Min(a[2], b[2])-math.Max(a[0], b[0]))
	iy := math.Max(0, math.Min(a[3], b[3])-math.Max(a[1], b[1]))
	area := math.Max(1, (a[2]-a[0])*(a[3]-a[1]))
	return ix * iy / area
}

// centering is 1 when box a's center sits on box b's center, falling to 0.75 at b's edge (breaks ties between
// overlapping people).
func centering(a, b []float64) float64 {
	ax, ay := (a[0]+a[2])/2, (a[1]+a[3])/2
	bx, by := (b[0]+b[2])/2, (b[1]+b[3])/2
	dx := math.Abs(ax-bx) / math.Max(1, (b[2]-b[0])/2)
	dy := math.Abs(ay-by) / math.Max(1, (b[3]-b[1])/2)
	return 1 - 0.25*math.Min(1.0, math.Max(dx, dy))
}

// gap is how far apart boxes a and b are: 0 when they touch or overlap, else the larger per-axis gap.
func gap(a, b []float64) float64 {
	return max(0, b[0]-a[2], a[0]-b[2], b[1]-a[3], a[1]-b[3])
}

func floats(v any) []float64 {
	a, _ := v.([]any)
	out := make([]float64, len(a))
	for i, x := range a {
		out[i] = pj.F(x)
	}
	return out
}

// PersonScore is how strongly the active AF points land on this person: head hits count double, torso 1.5, body 1,
// each scaled by how much of the point lies inside the region and how central it sits there.
//
// With near > 0, a point just beside a region still earns up to half its weight, falling to nothing at near
// point-widths away. Spot AF lands a point-width off a head (focus-and-recompose, a subject turning) often enough,
// and the focus plane still runs through that person.
func PersonScore(af pj.Obj, person pj.Obj, near float64) float64 {
	return Scores(af, []pj.Obj{person}, near, 0)[0]
}

// Scores is PersonScore for everyone in the frame at once, so people can be weighed against each other per point.
//
// With occlude > 0, a nearer person blocks the view of anyone behind them: when a point lies inside someone's box
// and that person is at least occlude times another's size (square root of box area; a bigger figure is nearer the
// camera), the point most likely landed on the near person's body, so the smaller one gets no credit for it. A point
// squarely on the smaller one's head still counts: their face is what's visible there.
func Scores(af pj.Obj, people []pj.Obj, near, occlude float64) []float64 {
	out := make([]float64, len(people))
	if len(af) == 0 || !pj.Truthy(af["active"]) {
		return out
	}
	byI := map[float64][]float64{}
	for _, pv := range pj.A(af, "points") {
		byI[pj.F(pj.Get(pv, "i"))] = floats(pj.Get(pv, "box"))
	}
	size := make([]float64, len(people))
	for k, p := range people {
		if bx := floats(p["box"]); len(bx) == 4 {
			size[k] = math.Sqrt(math.Max(0, (bx[2]-bx[0])*(bx[3]-bx[1])))
		}
	}
	hits := make([]float64, len(people))
	for _, i := range pj.A(af, "active") {
		b, ok := byI[pj.F(i)]
		if !ok {
			continue
		}
		reach := near * math.Max(1, b[2]-b[0])
		for k, person := range people {
			best := 0.0
			for _, r := range []struct {
				key string
				wgt float64
			}{{"head", 2.0}, {"torso", 1.5}, {"box", 1.0}} {
				if !pj.Truthy(person[r.key]) {
					continue
				}
				reg := floats(person[r.key])
				best = math.Max(best, r.wgt*overlap(b, reg)*centering(b, reg))
				if reach != 0 {
					best = math.Max(best, r.wgt*0.5*math.Max(0.0, 1-gap(b, reg)/reach))
				}
			}
			hits[k] = best
		}
		if occlude > 0 {
			for k, person := range people {
				if hits[k] == 0 || size[k] == 0 || (pj.Truthy(person["head"]) && overlap(b, floats(person["head"])) >= 0.5) {
					continue
				}
				for j, front := range people {
					if j != k && size[j] > 0 && size[j] >= occlude*size[k] && overlap(b, floats(front["box"])) >= 0.5 {
						hits[k] = 0
						break
					}
				}
			}
		}
		for k := range people {
			out[k] += hits[k]
		}
	}
	for k := range out {
		out[k] = pj.Round(out[k], 3)
	}
	return out
}
