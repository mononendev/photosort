// Package truth reads ground truth for calibration: the photographer's own verdicts, exported from Lightroom/Bridge
// as XMP sidecars (Save Metadata to File), a metadata XML, or a CSV, matched to images by filename stem.
//
// A verdict is {"rating": 0-5|None, "label": str|None, "focus_tier": 0-3|None, "keywords": [...]}. focus_tier is
// taken from, in order: an explicit CSV column; a keyword like "focus3" / "focus:1" / "tier0"; the color label via
// cfg["truth"]["label_tiers"]; the star rating via cfg["truth"]["rating_tiers"].
package truth

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
	"github.com/mononendev/photosort/internal/sidecar"
	"github.com/mononendev/photosort/internal/stats"
)

// Tiers are the focus tiers: 0 miss, 1 soft, 2 slightly soft, 3 sharp.
var Tiers = []int{0, 1, 2, 3}

var (
	subjectRe = regexp.MustCompile(`(?s)<dc:subject>(.*?)</dc:subject>`)
	liRe      = regexp.MustCompile(`(?s)<rdf:li[^>]*>(.*?)</rdf:li>`)
	// Python's re.match(r"^(?:focus|tier)[:_ -]?([0-3])$", re.I): its $ also matches before a final newline.
	focusKwRe = regexp.MustCompile(`(?i)^(?:focus|tier)[:_ -]?([0-3])\n?$`)
)

// Verdict is one image's ground truth.
type Verdict struct {
	Rating    *int
	Label     *string
	FocusTier *int
	Keywords  []string
	fromCSV   bool // key order: an XMP verdict has keywords before focus_tier, a CSV one after
}

// PyObject is the verdict as the Python dict parse_xmp / parse_csv built (the key order truth_json is stored with).
func (v Verdict) PyObject() *py.Object {
	kws := v.Keywords
	if kws == nil {
		kws = []string{}
	}
	o := py.NewObject("rating", intOrNil(v.Rating), "label", strOrNil(v.Label))
	if v.fromCSV {
		o.Set("focus_tier", intOrNil(v.FocusTier))
		o.Set("keywords", kws)
	} else {
		o.Set("keywords", kws)
		o.Set("focus_tier", intOrNil(v.FocusTier))
	}
	return o
}

// JSON is the truth_json text: json.dumps of the verdict.
func (v Verdict) JSON() string { return py.Dumps(v) }

// MarshalJSON writes the verdict in its Python key order.
func (v Verdict) MarshalJSON() ([]byte, error) { return v.PyObject().MarshalJSON() }

func intOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func strOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// ParseXMP reads rating, label and the dc:subject keywords (stripped, empty ones dropped) from XMP text.
func ParseXMP(text string) Verdict {
	rating, label := sidecar.RatingLabel(text)
	kws := []string{}
	for _, block := range subjectRe.FindAllStringSubmatch(text, -1) {
		for _, li := range liRe.FindAllStringSubmatch(block[1], -1) {
			if k := py.Strip(li[1]); k != "" {
				kws = append(kws, k)
			}
		}
	}
	return Verdict{Rating: rating, Label: label, Keywords: kws}
}

// ParseCSV reads a CSV with columns (case-insensitive) name|file|path|filename (required), rating, label,
// focus_tier|tier, keywords (split on , or ;). Keyed by lowercased filename stem; a later row wins. It fails where
// Python's csv module raises.
func ParseCSV(text string) (map[string]Verdict, error) {
	recs, err := py.ReadCSV(text)
	if err != nil {
		return nil, err
	}
	out := map[string]Verdict{}
	if len(recs) == 0 {
		return out, nil
	}
	header := recs[0] // DictReader: the first record, even a blank one
	for _, rec := range recs[1:] {
		if len(rec) == 0 {
			continue
		}
		// dict(zip(fieldnames, row)): a repeated header keeps its first position and last value; extra values go
		// under the None key and missing ones are None, which the comprehension drops or blanks. The comprehension
		// then normalizes keys in that order, so of two headers that normalize alike the later position wins.
		zipped := py.NewObject()
		for i, k := range header {
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			zipped.Set(k, v)
		}
		row := map[string]string{}
		for _, k := range zipped.Keys() {
			if k == "" {
				continue
			}
			v, _ := zipped.Get(k)
			row[py.Lower(py.Strip(k))] = py.Strip(v.(string))
		}
		name := firstNonEmpty(row["name"], row["file"], row["path"], row["filename"])
		if name == "" {
			continue
		}
		v := Verdict{Label: nonEmpty(row["label"]), Keywords: []string{}, fromCSV: true}
		if rs := row["rating"]; py.IsDigit(strings.TrimLeft(rs, "-")) {
			n, ok := py.Int(rs)
			if !ok {
				return nil, fmt.Errorf("invalid literal for int() with base 10: %s", py.Repr(rs))
			}
			r := int(n)
			v.Rating = &r
		}
		tier := firstNonEmpty(row["focus_tier"], row["tier"])
		if len(tier) == 1 && tier[0] >= '0' && tier[0] <= '3' {
			t := int(tier[0] - '0')
			v.FocusTier = &t
		}
		for _, k := range strings.Split(strings.ReplaceAll(row["keywords"], ";", ","), ",") {
			if k = py.Strip(k); k != "" {
				v.Keywords = append(v.Keywords, k)
			}
		}
		out[py.Lower(py.Stem(name))] = v
	}
	return out, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// File is one uploaded or found file.
type File struct {
	Name string
	Data []byte
}

// ParseFiles turns files into {stem: verdict}: zips are expanded (recursively), .xmp/.xml parsed as XMP, .csv as
// CSV, anything else ignored. Text is decoded as UTF-8 with invalid bytes dropped. A later file wins a stem.
func ParseFiles(files []File) (map[string]Verdict, error) {
	out := map[string]Verdict{}
	for _, f := range files {
		low := py.Lower(f.Name)
		switch {
		case strings.HasSuffix(low, ".zip"):
			inner, err := unzip(f.Data)
			if err != nil {
				return nil, err
			}
			m, err := ParseFiles(inner)
			if err != nil {
				return nil, err
			}
			for k, v := range m {
				out[k] = v
			}
		case strings.HasSuffix(low, ".csv"):
			m, err := ParseCSV(py.ValidUTF8(string(f.Data)))
			if err != nil {
				return nil, err
			}
			for k, v := range m {
				out[k] = v
			}
		case strings.HasSuffix(low, ".xmp"), strings.HasSuffix(low, ".xml"):
			out[py.Lower(py.Stem(f.Name))] = ParseXMP(py.ValidUTF8(string(f.Data)))
		}
	}
	return out, nil
}

// unzip reads every file entry (not directories) as zipfile does: names decoded as UTF-8 when the entry says so,
// else as cp437, cut at a NUL.
func unzip(data []byte) ([]File, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("bad zip file: %w", err)
	}
	var out []File
	for _, f := range z.File {
		name := f.Name
		if f.Flags&0x800 == 0 {
			name = cp437(name)
		}
		if i := strings.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, File{Name: name, Data: b})
	}
	return out, nil
}

const cp437High = "\u00c7\u00fc\u00e9\u00e2\u00e4\u00e0\u00e5\u00e7\u00ea\u00eb\u00e8\u00ef\u00ee\u00ec\u00c4\u00c5\u00c9\u00e6\u00c6\u00f4\u00f6\u00f2\u00fb\u00f9\u00ff\u00d6\u00dc\u00a2\u00a3\u00a5\u20a7\u0192\u00e1\u00ed\u00f3\u00fa\u00f1\u00d1\u00aa\u00ba\u00bf\u2310\u00ac\u00bd\u00bc\u00a1\u00ab\u00bb\u2591\u2592\u2593\u2502\u2524\u2561\u2562\u2556\u2555\u2563\u2551\u2557\u255d\u255c\u255b\u2510\u2514\u2534\u252c\u251c\u2500\u253c\u255e\u255f\u255a\u2554\u2569\u2566\u2560\u2550\u256c\u2567\u2568\u2564\u2565\u2559\u2558\u2552\u2553\u256b\u256a\u2518\u250c\u2588\u2584\u258c\u2590\u2580\u03b1\u00df\u0393\u03c0\u03a3\u03c3\u00b5\u03c4\u03a6\u0398\u03a9\u03b4\u221e\u03c6\u03b5\u2229\u2261\u00b1\u2265\u2264\u2320\u2321\u00f7\u2248\u00b0\u2219\u00b7\u221a\u207f\u00b2\u25a0\u00a0"

var cp437Table = []rune(cp437High)

func cp437(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x80 {
			b.WriteByte(c)
		} else {
			b.WriteRune(cp437Table[c-0x80])
		}
	}
	return b.String()
}

// defaultTruth is DEFAULTS["truth"], for a config without a "truth" section.
func defaultTruth() any { return pj.Get(config.Defaults(), "truth") }

// ResolveTier picks a verdict's focus tier: its explicit tier, else a focus keyword, else the label's tier from
// cfg["truth"]["label_tiers"], else the rating's from cfg["truth"]["rating_tiers"] (only a valid tier counts).
// nil when none applies.
func ResolveTier(v Verdict, cfg pj.Obj) *int {
	if v.FocusTier != nil && *v.FocusTier >= 0 && *v.FocusTier <= 3 {
		t := *v.FocusTier
		return &t
	}
	for _, k := range v.Keywords {
		if m := focusKwRe.FindStringSubmatch(k); m != nil {
			t := int(m[1][0] - '0')
			return &t
		}
	}
	t, ok := cfg["truth"]
	if !ok {
		t = defaultTruth()
	}
	if v.Label != nil && *v.Label != "" {
		if lt := pj.O(t, "label_tiers"); lt != nil {
			if val, ok := lt[*v.Label]; ok {
				// Python returns the configured value as is; config tiers are whole numbers.
				if n, ok := py.IntKey(val); ok {
					return &n
				}
				return nil
			}
		}
	}
	if v.Rating != nil {
		r := pj.Get(t, "rating_tiers", strconv.Itoa(*v.Rating))
		if n, ok := py.IntKey(r); ok && n >= 0 && n <= 3 {
			return &n
		}
		return nil
	}
	return nil
}

// Row is a tracked image to match by stem.
type Row struct {
	ID   int64
	Path string
}

// Item is a verdict to store for an image (truth_json = Verdict.JSON()).
type Item struct {
	ID      int64
	Verdict Verdict
}

// Counts is apply's summary.
type Counts struct {
	Verdicts  int `json:"verdicts"`
	Matched   int `json:"matched"`
	Unmatched int `json:"unmatched"`
}

// Apply attaches verdicts to tracked images by filename stem, each with its tier resolved, and hands them to set.
// rows are the images to consider (the caller narrows them to a folder, as db.under_folder did).
func Apply(rows []Row, verdicts map[string]Verdict, cfg pj.Obj, set func(items []Item) error) (Counts, error) {
	var found []Item
	seen := map[string]bool{}
	for _, r := range rows {
		stem := py.Lower(py.Stem(r.Path))
		v, ok := verdicts[stem]
		if !ok {
			continue
		}
		v.FocusTier = ResolveTier(v, cfg)
		found = append(found, Item{ID: r.ID, Verdict: v})
		seen[stem] = true
	}
	if err := set(found); err != nil {
		return Counts{}, err
	}
	unmatched := 0
	for k := range verdicts {
		if !seen[k] {
			unmatched++
		}
	}
	return Counts{Verdicts: len(verdicts), Matched: len(found), Unmatched: unmatched}, nil
}

// Where a photo's truth tier comes from: your in-app rating (q/w/e/r/t; a banger counts as sharp), the verdicts you
// imported, or both, with your rating winning where a photo has both.
const (
	RatedTierSQL    = "CASE WHEN json_extract(override_json,'$.reviewed') THEN MIN(json_extract(override_json,'$.rating'), 3) END"
	ImportedTierSQL = "json_extract(truth_json,'$.focus_tier')"
)

// TruthSources maps a source name to the SQL for its truth tier.
var TruthSources = map[string]string{
	"both":     "COALESCE(" + RatedTierSQL + ", " + ImportedTierSQL + ")",
	"ratings":  RatedTierSQL,
	"imported": ImportedTierSQL,
}

// Metric is which stored per-image value a threshold set calibrates: its local_json path, and the config keys of
// its tier 3, 2 and 1 cuts.
type Metric struct {
	Path string
	Keys [3]string
}

// Metrics are the calibratable metrics.
var Metrics = map[string]Metric{
	"head": {"$.primary_head_sharp", [3]string{"tier3_min", "tier2_min", "tier1_min"}},
	"eye":  {"$.primary_eye_sharp", [3]string{"eye_tier3_min", "eye_tier2_min", "eye_tier1_min"}},
	"hf":   {"$.primary_eye_hf", [3]string{"hf_tier3_min", "hf_tier2_min", "hf_tier1_min"}},
}

// MetricNames is sorted(METRICS), for error messages.
func MetricNames() []string {
	out := make([]string, 0, len(Metrics))
	for k := range Metrics {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Pair is one photo's metric value and truth tier.
type Pair struct {
	Value float64
	Tier  int
}

// Suggestion is a suggested cut for "tier N or better".
type Suggestion struct {
	Tier             int
	Value            float64 // rounded to 4 places
	BalancedAccuracy float64 // rounded to 3 places
}

// Key is the head metric's config key for the cut: tier3_min, tier2_min or tier1_min.
func (s Suggestion) Key() string { return fmt.Sprintf("tier%d_min", s.Tier) }

// Suggestions are the cuts found, tier 3 first.
type Suggestions []Suggestion

// PyObject is {"tier3_min": {"value", "balanced_accuracy"}, ...} in tier 3, 2, 1 order.
func (ss Suggestions) PyObject() *py.Object {
	o := py.NewObject()
	for _, s := range ss {
		o.Set(s.Key(), py.NewObject("value", s.Value, "balanced_accuracy", s.BalancedAccuracy))
	}
	return o
}

// MarshalJSON writes the Python dict's shape and order.
func (ss Suggestions) MarshalJSON() ([]byte, error) { return ss.PyObject().MarshalJSON() }

// SuggestThresholds grid-searches, per tier, the cut on the metric that best separates tier-N-or-better from the
// rest by balanced accuracy, over 97 candidate cuts (the metric's 2nd..98th percentiles). A tier with fewer than 3
// photos on either side is left out; fewer than 10 pairs gives nothing. Cuts come out ordered (tier1 <= tier2 <=
// tier3): with few photos a lower tier's best cut can land above a higher one's, and is then pulled down to it.
func SuggestThresholds(pairs []Pair) Suggestions {
	out := Suggestions{}
	if len(pairs) < 10 {
		return out
	}
	x := make([]float64, len(pairs))
	for i, p := range pairs {
		x[i] = p.Value
	}
	cands := stats.Unique(stats.Quantiles(x, stats.Linspace(0.02, 0.98, 97)))

	score := func(t int, c float64) float64 {
		var pos, posHit, neg, negHit int
		for _, p := range pairs {
			if p.Tier >= t {
				pos++
				if p.Value >= c {
					posHit++
				}
			} else {
				neg++
				if p.Value < c {
					negHit++
				}
			}
		}
		return (float64(posHit)/float64(pos) + float64(negHit)/float64(neg)) / 2
	}
	ceiling := 0.0
	hasCeiling := false
	for _, t := range []int{3, 2, 1} {
		pos := 0
		for _, p := range pairs {
			if p.Tier >= t {
				pos++
			}
		}
		if pos < 3 || len(pairs)-pos < 3 {
			continue
		}
		scores := make([]float64, len(cands))
		for i, c := range cands {
			scores[i] = score(t, c)
		}
		c := cands[stats.Argmax(scores)]
		if hasCeiling && ceiling < c { // min(c, ceiling)
			c = ceiling
		}
		s := Suggestion{Tier: t, Value: pj.Round(c, 4), BalancedAccuracy: pj.Round(score(t, c), 3)}
		out = append(out, s)
		ceiling, hasCeiling = s.Value, true
	}
	return out
}
