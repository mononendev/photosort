package export

import (
	"os"
	"strings"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// xmpTemplate is the sidecar written for each record; the placeholders are filled in one pass, as str.format does.
const xmpTemplate = `<?xpacket begin="` + "\ufeff" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="photosort">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about=""
    xmlns:dc="http://purl.org/dc/elements/1.1/"
    xmlns:xmp="http://ns.adobe.com/xap/1.0/"
    xmlns:lr="http://ns.adobe.com/lightroom/1.0/"
    xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"
    xmlns:photosort="http://photosort.local/ns/1.0/"
    {rating}
    {label}
    {attrs}>
   <dc:subject><rdf:Bag>
{subjects}
   </rdf:Bag></dc:subject>
   <lr:hierarchicalSubject><rdf:Bag>
{hier}
   </rdf:Bag></lr:hierarchicalSubject>
   <dc:description><rdf:Alt><rdf:li xml:lang="x-default">{description}</rdf:li></rdf:Alt></dc:description>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>
`

// escape is xml.sax.saxutils.escape: & < > (and " in attribute values).
var (
	escText = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	escAttr = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

func tierName(v any) string {
	if n, ok := py.IntKey(v); ok {
		if s, ok := TierNames[n]; ok {
			return s
		}
	}
	return "unknown"
}

func list(v any) []any {
	items, _ := py.List(py.Or(v, []any{}))
	return items
}

// photosortTags are the flat tags (focus-, subject-, comp-, action-, photosort-review/-banger and the group's leaf
// keywords) and the hierarchical keywords (PhotoSort|Focus|..., ... and the group's keywords) of a record.
func photosortTags(r *Record, groups pj.Obj) (tags, hier []string) {
	tn := tierName(r.FocusTier)
	subject, comp := py.Str(r.Subject), py.Str(r.Composition)
	tags = []string{"focus-" + tn, "subject-" + subject, "comp-" + comp}
	if py.Truthy(r.Action) && !py.Eq(r.Action, "none") {
		tags = append(tags, "action-"+py.Str(r.Action))
	}
	hier = []string{"PhotoSort|Focus|" + tn, "PhotoSort|Subject|" + subject, "PhotoSort|Composition|" + comp}
	if r.Review {
		tags = append(tags, "photosort-review")
		hier = append(hier, "PhotoSort|Review")
	}
	if r.Banger {
		tags = append(tags, "photosort-banger")
		hier = append(hier, "PhotoSort|Banger")
	}
	for _, kw := range list(pj.Get(groupEntry(r, groups), "keywords")) { // your group's keywords, as Lightroom keywords
		if k := strings.Trim(cfgStr(kw), "| "); k != "" {
			parts := strings.Split(k, "|")
			tags = append(tags, parts[len(parts)-1])
			hier = append(hier, k)
		}
	}
	return tags, hier
}

// XMPFor renders a record's XMP sidecar: focus/subject/composition (and action, review, banger, group) keywords
// flat and under PhotoSort|..., the model's keywords and adjectives, stars from the quality score, the color label
// of your rating, the tiers and notes as photosort:/photoshop: attributes, and the description. groups is the
// config's "groups": a grouped record gets its group's keywords ("Parent|Child" ones as a hierarchy).
func XMPFor(r *Record, groups pj.Obj) string {
	tags, hier := photosortTags(r, groups)
	kws := append(append([]any{}, list(r.Keywords)...), list(r.Adjectives)...)
	for _, t := range tags {
		kws = append(kws, t)
	}
	li := func(xs []string) string {
		lines := make([]string, len(xs))
		for i, x := range xs {
			lines[i] = "    <rdf:li>" + escText.Replace(x) + "</rdf:li>"
		}
		return strings.Join(lines, "\n")
	}
	subjects := make([]string, len(kws))
	for i, k := range kws {
		subjects[i] = py.Str(k)
	}

	var notes []string
	for _, x := range []any{r.FocusNotes, r.QualityRemarks} {
		if py.Truthy(x) {
			notes = append(notes, py.Str(x))
		}
	}
	var keeper, instr any
	if r.Keeper != nil {
		keeper = py.Lower(py.Str(r.Keeper))
	}
	if s := strings.Join(notes, " "); s != "" {
		instr = s
	}
	attrs := []struct {
		k string
		v any
	}{
		{"photosort:FocusTier", r.FocusTier}, {"photosort:FocusTierLocal", r.FocusTierLocal},
		{"photosort:FocusTierVLM", r.FocusTierVLM}, {"photosort:QualityScore", r.QualityScore},
		{"photosort:Rating", r.Rating}, {"photosort:Keeper", keeper}, {"photoshop:Instructions", instr},
	}
	var attrLines []string
	for _, a := range attrs {
		if a.v != nil {
			attrLines = append(attrLines, a.k+`="`+escAttr.Replace(py.Str(a.v))+`"`)
		}
	}
	rating, label := "", ""
	if py.Truthy(r.QualityScore) {
		rating = `xmp:Rating="` + py.Str(r.QualityScore) + `"`
	}
	if n, ok := py.IntKey(r.Rating); ok {
		if l, ok := RatingLabels[n]; ok {
			label = `xmp:Label="` + l + `"`
		}
	}
	return strings.NewReplacer(
		"{rating}", rating,
		"{label}", label,
		"{attrs}", strings.Join(attrLines, "\n    "),
		"{subjects}", li(subjects),
		"{hier}", li(hier),
		"{description}", escText.Replace(py.Str(py.Or(r.Description, ""))),
	).Replace(xmpTemplate)
}

// WriteXMP writes an XMP sidecar for each record with a tier: into xmpDir as <stem>.xmp, or next to the image
// (its suffix replaced by .xmp) when xmpDir is "". An existing sidecar is kept unless overwrite. Returns how many
// were written and skipped.
func WriteXMP(records []*Record, xmpDir string, overwrite bool, groups pj.Obj) (written, skipped int, err error) {
	for _, r := range records {
		if r.FocusTier == nil {
			continue
		}
		dst := py.WithSuffix(r.Path, ".xmp")
		if xmpDir != "" {
			dst = py.Join(xmpDir, py.Stem(r.Path)+".xmp")
		}
		if _, err := os.Stat(dst); err == nil && !overwrite {
			skipped++
			continue
		}
		if err := os.MkdirAll(py.Parent(dst), 0o777); err != nil {
			return written, skipped, err
		}
		if err := os.WriteFile(dst, []byte(XMPFor(r, groups)), 0o666); err != nil {
			return written, skipped, err
		}
		written++
	}
	return written, skipped, nil
}
