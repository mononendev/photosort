package export

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// groupEntry is the config entry for the record's sort group (config "groups"), or nil when it has none.
func groupEntry(r *Record, groups pj.Obj) any {
	if !py.Truthy(r.Group) {
		return nil
	}
	return groups[py.Str(r.Group)]
}

// cfgStr is str() of a config value. The config is decoded without Python's int/float distinction, so a whole
// number prints as an int, as the Python config's ints did.
func cfgStr(v any) string {
	if f, ok := v.(float64); ok {
		if n, ok := py.IntKey(f); ok {
			return py.Str(int64(n))
		}
	}
	return py.Str(v)
}

// GroupFolder is the tree folder a grouped record goes under: the last component of its group's configured folder,
// else group_N. "" when the record has no group.
func GroupFolder(r *Record, groups pj.Obj) string {
	if !py.Truthy(r.Group) {
		return ""
	}
	folder := pj.Get(groupEntry(r, groups), "folder")
	if name := py.Name(cfgStr(py.Or(folder, ""))); name != "" {
		return name
	}
	return "group_" + py.Str(r.Group)
}

// Place puts src at dst by mode: "symlink" (to src's resolved absolute path), "hardlink", "copy" (content,
// permissions and times, as shutil.copy2) or "move"; any other mode does nothing. The parent folders are created and
// an existing dst (file or link) replaced.
func Place(src, dst, mode string) error {
	if err := os.MkdirAll(py.Parent(dst), 0o777); err != nil {
		return err
	}
	if fi, err := os.Lstat(dst); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("unlink %s: is a directory", dst)
		}
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	switch mode {
	case "symlink":
		return os.Symlink(resolve(src), dst)
	case "hardlink":
		return os.Link(src, dst)
	case "copy":
		return copy2(src, dst)
	case "move":
		return move(src, dst)
	}
	return nil
}

// resolve is Path.resolve() (non-strict): absolute, symlinks resolved as far as the path exists, the rest appended.
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	rest := ""
	for dir := abs; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// copy2 is shutil.copy2: the content, then the permission bits and access/modification times.
func copy2(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(dst, atime(fi), fi.ModTime()); err != nil {
		return err
	}
	return os.Chmod(dst, fi.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
}

// move is shutil.move for a file: a rename, or where that fails (another file system) a copy2 and delete; a symlink
// is recreated rather than followed.
func move(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if fi, err := os.Lstat(src); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		to, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(to, dst); err != nil {
			return err
		}
		return os.Remove(src)
	}
	if err := copy2(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// Count is one tree folder's image count.
type Count struct {
	Key string
	N   int
}

// Counts are BuildTree's per-folder counts in first-placed order (the Python dict's).
type Counts []Count

func (c *Counts) add(key string) {
	for i := range *c {
		if (*c)[i].Key == key {
			(*c)[i].N++
			return
		}
	}
	*c = append(*c, Count{key, 1})
}

// Get is a folder's count, 0 when absent.
func (c Counts) Get(key string) int {
	for _, x := range c {
		if x.Key == key {
			return x.N
		}
	}
	return 0
}

// PyObject is the counts as the Python dict.
func (c Counts) PyObject() *py.Object {
	o := py.NewObject()
	for _, x := range c {
		o.Set(x.Key, x.N)
	}
	return o
}

// MarshalJSON writes {"folder": n, ...} in order.
func (c Counts) MarshalJSON() ([]byte, error) { return c.PyObject().MarshalJSON() }

// String is the Python dict's repr, as the CLI prints it.
func (c Counts) String() string { return py.Repr(c.PyObject()) }

// BuildTree places each record's image (existing, with a tier) under out/<tier>/<subject>/<composition>/, or
// out/<tier>/ when it has no subject info yet (a local-only run); a grouped image goes inside its group's folder.
// Disagreements are also placed under review/local<L>_vlm<V>/ and bangers under bangers/ (as symlinks when moving).
// Returns the counts per folder.
func BuildTree(records []*Record, out, mode string, groups pj.Obj) (Counts, error) {
	counts := Counts{}
	aside := mode
	if mode == "move" {
		aside = "symlink"
	}
	for _, r := range records {
		src := r.Path
		if _, err := os.Stat(src); err != nil || r.FocusTier == nil {
			continue
		}
		t, ok := py.IntKey(r.FocusTier)
		tierDir, ok2 := TierNames[t]
		if !ok || !ok2 {
			return counts, fmt.Errorf("no tier folder for focus_tier %s", py.Repr(r.FocusTier))
		}
		gdir := GroupFolder(r, groups)
		base := out
		if gdir != "" {
			base = py.Join(out, gdir)
		}
		var dst string
		if py.Eq(r.Subject, "unknown") && py.Eq(r.Composition, "unknown") {
			dst = py.Join(base, tierDir, py.Name(src))
		} else {
			subj, ok1 := r.Subject.(string)
			comp, ok2 := r.Composition.(string)
			if !ok1 || !ok2 {
				return counts, fmt.Errorf("%s: subject %s / composition %s is not a folder name", src, py.Repr(r.Subject), py.Repr(r.Composition))
			}
			dst = py.Join(base, tierDir, subj, comp, py.Name(src))
		}
		if err := Place(src, dst, mode); err != nil {
			return counts, err
		}
		key := tierDir
		if gdir != "" {
			key = gdir + "/" + tierDir
		}
		counts.add(key)
		if r.Review {
			why := "local" + py.Str(r.FocusTierLocal) + "_vlm" + py.Str(r.FocusTierVLM)
			if err := Place(src, py.Join(out, "review", why, py.Name(src)), aside); err != nil {
				return counts, err
			}
			counts.add("review")
		}
		if r.Banger {
			if err := Place(src, py.Join(out, "bangers", py.Name(src)), aside); err != nil {
				return counts, err
			}
			counts.add("bangers")
		}
	}
	return counts, nil
}

// CSVColumns are results.csv's columns, in order.
var CSVColumns = []string{"path", "focus_tier", "focus_tier_local", "focus_tier_vlm", "review", "subject", "composition",
	"placement", "action", "people_count", "rating", "reviewed", "group", "quality_score", "keeper", "keywords",
	"adjectives", "description", "focus_notes", "quality_remarks", "error"}

// joinList is "; ".join(v or []).
func joinList(v any) (string, error) {
	items, ok := py.List(py.Or(v, []any{}))
	if !ok {
		return "", fmt.Errorf("can't join %s", py.Repr(v))
	}
	parts := make([]string, len(items))
	for i, it := range items {
		s, ok := it.(string)
		if !ok {
			return "", fmt.Errorf("sequence item %d: expected str instance, got %s", i, py.Repr(it))
		}
		parts[i] = s
	}
	return strings.Join(parts, "; "), nil
}

// JSONL is results.jsonl's content: json.dumps of each record, one per line.
func JSONL(records []*Record) string {
	var b strings.Builder
	for _, r := range records {
		b.WriteString(py.Dumps(r))
		b.WriteByte('\n')
	}
	return b.String()
}

// CSV is results.csv's content: CSVColumns with keywords and adjectives joined by "; ", in Python's csv dialect.
func CSV(records []*Record) (string, error) {
	var b strings.Builder
	py.WriteCSVRow(&b, CSVColumns)
	for _, r := range records {
		o := r.PyObject()
		kw, err := joinList(r.Keywords)
		if err != nil {
			return "", err
		}
		adj, err := joinList(r.Adjectives)
		if err != nil {
			return "", err
		}
		o.Set("keywords", kw)
		o.Set("adjectives", adj)
		fields := make([]string, len(CSVColumns))
		for i, c := range CSVColumns {
			v := o.Get(c)
			fields[i] = py.CSVField(v)
		}
		py.WriteCSVRow(&b, fields)
	}
	return b.String(), nil
}

// Export writes out/results.jsonl and out/results.csv.
func Export(records []*Record, out string) error {
	if err := os.MkdirAll(out, 0o777); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "results.jsonl"), []byte(JSONL(records)), 0o666); err != nil {
		return err
	}
	c, err := CSV(records)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "results.csv"), []byte(c), 0o666)
}
