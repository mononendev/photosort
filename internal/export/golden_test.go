package export

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// testdata/export_golden.json holds the Python implementation's outputs (photosort/sort.py, truth.py, sidecar.py,
// numpy) for the same inputs, written by a one-off generator run against the old package (not kept in the repo).
// Paths in it are "{ROOT}/..." placeholders for the temp dir both sides work in.
type listingEntry [3]string // relative path, "file"|"link", content or link target

type golden struct {
	Groups      pj.Obj                `json:"groups"`
	Rows        []map[string]*string  `json:"rows"`
	BadRows     []map[string]*string  `json:"bad_rows"`
	Records     map[string][]string   `json:"records"`
	XMP         map[string][]string   `json:"xmp"`
	XMPNoGroups []string              `json:"xmp_nogroups"`
	JSONL       string                `json:"jsonl"`
	CSV         string                `json:"csv"`
	Tree        map[string]goldenTree `json:"tree"`
	WriteXMP    struct {
		First        [2]int         `json:"first"`
		Second       [2]int         `json:"second"`
		Sidecar      [2]int         `json:"sidecar"`
		Files        []listingEntry `json:"files"`
		SidecarFiles []string       `json:"sidecar_files"`
	} `json:"write_xmp"`
}

type goldenTree struct {
	Counts [][]any        `json:"counts"`
	Repr   string         `json:"repr"`
	Files  []listingEntry `json:"files"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile("../../testdata/export_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func tempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir()) // Python resolved its temp dir (/var -> /private/var on macOS)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func toRow(m map[string]*string, root string) Row {
	path := strings.ReplaceAll(*m["path"], "{ROOT}", root)
	return Row{Path: path, Error: m["error"], LocalJSON: m["local_json"], VLMJSON: m["vlm_json"], OverrideJSON: m["override_json"]}
}

func listing(t *testing.T, dir, root string) []listingEntry {
	t.Helper()
	var out []listingEntry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.Type()&fs.ModeSymlink != 0 {
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out = append(out, listingEntry{rel, "link", strings.ReplaceAll(to, root, "{ROOT}")})
			return nil
		}
		b, err := os.ReadFile(p)
		out = append(out, listingEntry{rel, "file", string(b)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func eqListing(t *testing.T, name string, got, want []listingEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d files, want %d\n got %q\nwant %q", name, len(got), len(want), got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %q, want %q", name, i, got[i], want[i])
		}
	}
}

func eqCounts(t *testing.T, name string, got Counts, want [][]any) {
	t.Helper()
	w := Counts{}
	for _, kv := range want {
		w = append(w, Count{kv[0].(string), int(kv[1].(float64))})
	}
	if len(got) != len(w) {
		t.Errorf("%s: counts %v, want %v", name, got, w)
		return
	}
	for i := range got {
		if got[i] != w[i] {
			t.Errorf("%s: counts %v, want %v", name, got, w)
			return
		}
	}
}

func TestGoldenAgainstPython(t *testing.T) {
	g := loadGolden(t)
	root := tempRoot(t)
	unsub := func(s string) string { return strings.ReplaceAll(s, root, "{ROOT}") }
	if len(g.Rows) < 15 || len(g.Records["strict"]) != len(g.Rows) || len(g.Tree) != 4 || len(g.BadRows) == 0 ||
		g.CSV == "" || len(g.WriteXMP.Files) == 0 {
		t.Fatal("golden file is incomplete")
	}

	for _, m := range g.BadRows {
		if _, err := FinalRecord(toRow(m, root), "vlm"); err == nil {
			t.Errorf("%s: no error, Python raised", *m["path"])
		}
	}

	recs := map[string][]*Record{}
	for _, source := range []string{"vlm", "local", "strict"} {
		for i, m := range g.Rows {
			rec, err := FinalRecord(toRow(m, root), source)
			if err != nil {
				t.Fatalf("%s %s: %v", source, *m["path"], err)
			}
			recs[source] = append(recs[source], rec)
			if got, want := unsub(py.Dumps(rec)), g.Records[source][i]; got != want {
				t.Errorf("record %s %d:\n got %s\nwant %s", source, i, got, want)
			}
			if got, want := XMPFor(rec, g.Groups), g.XMP[source][i]; got != want {
				t.Errorf("xmp %s %d:\n got %q\nwant %q", source, i, got, want)
			}
		}
	}
	for i, rec := range recs["vlm"] {
		if got, want := XMPFor(rec, nil), g.XMPNoGroups[i]; got != want {
			t.Errorf("xmp without groups %d:\n got %q\nwant %q", i, got, want)
		}
	}

	exp := filepath.Join(root, "export")
	if err := Export(recs["vlm"], exp); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"results.jsonl": g.JSONL, "results.csv": g.CSV} {
		b, err := os.ReadFile(filepath.Join(exp, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := unsub(string(b)); got != want {
			t.Errorf("%s differs:\n got %q\nwant %q", name, got, want)
		}
	}

	for _, rec := range recs["vlm"] {
		if py.Name(rec.Path) == "missing.jpg" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(rec.Path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rec.Path, []byte("img:"+py.Name(rec.Path)), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"copy", "symlink", "hardlink"} {
		d := filepath.Join(root, "tree_"+mode)
		counts, err := BuildTree(recs["vlm"], d, mode, g.Groups)
		if err != nil {
			t.Fatal(mode, err)
		}
		want := g.Tree[mode]
		eqCounts(t, mode, counts, want.Counts)
		if counts.String() != want.Repr {
			t.Errorf("%s repr: got %s, want %s", mode, counts, want.Repr)
		}
		eqListing(t, "tree "+mode, listing(t, d, root), want.Files)
	}
	counts, err := BuildTree(recs["strict"], filepath.Join(root, "tree_copy"), "copy", g.Groups)
	if err != nil {
		t.Fatal(err)
	}
	eqCounts(t, "copy again", counts, g.Tree["copy_again_strict"].Counts)
	eqListing(t, "tree copy again", listing(t, filepath.Join(root, "tree_copy"), root), g.Tree["copy_again_strict"].Files)

	xd := filepath.Join(root, "xmpout")
	for _, c := range []struct {
		name      string
		dir       string
		overwrite bool
		want      [2]int
	}{{"first", xd, false, g.WriteXMP.First}, {"second", xd, false, g.WriteXMP.Second}, {"sidecar", "", true, g.WriteXMP.Sidecar}} {
		w, s, err := WriteXMP(recs["vlm"], c.dir, c.overwrite, g.Groups)
		if err != nil {
			t.Fatal(err)
		}
		if [2]int{w, s} != c.want {
			t.Errorf("write_xmp %s: (%d, %d), want %v", c.name, w, s, c.want)
		}
	}
	eqListing(t, "xmp dir", listing(t, xd, root), g.WriteXMP.Files)
	var sidecars []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".xmp") && !strings.Contains(p, "xmpout") {
			sidecars = append(sidecars, unsub(p))
		}
		return nil
	})
	sort.Strings(sidecars)
	if strings.Join(sidecars, "\n") != strings.Join(g.WriteXMP.SidecarFiles, "\n") {
		t.Errorf("sidecars: got %q, want %q", sidecars, g.WriteXMP.SidecarFiles)
	}
}
