package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// Image is a row of the images table. Which fields are filled depends on the columns a query selected.
type Image struct {
	ID           int64
	Path         string
	Folder       *string
	Size         *int64
	Mtime        *float64
	LocalJSON    *string
	BatchID      *string
	VLMJSON      *string
	VLMUsage     *string
	VLMSkip      *string
	OverrideJSON *string
	Error        *string
	LocalAt      *float64
	VLMAt        *float64
	LRJSON       *string
	TruthJSON    *string
}

// ImageCols are all the columns, in the order Image lists them.
const ImageCols = "id, path, folder, size, mtime, local_json, batch_id, vlm_json, vlm_usage, vlm_skip, override_json, " +
	"error, local_at, vlm_at, lr_json, truth_json"

// jsonText reads a JSON column as text whatever the database stores it as (JSONB arrives as bytes from pgx).
type jsonText struct{ p **string }

func (j jsonText) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*j.p = nil
	case string:
		*j.p = &v
	case []byte:
		s := string(v)
		*j.p = &s
	default:
		return fmt.Errorf("json column: unexpected %T", src)
	}
	return nil
}

// ScanImages reads every row into an Image, matching fields by column name; extra columns land in extra, when given.
func ScanImages(rows *sql.Rows, extra func(name string) any) ([]Image, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Image
	for rows.Next() {
		var im Image
		dest := make([]any, len(cols))
		for i, c := range cols {
			switch c {
			case "id":
				dest[i] = &im.ID
			case "path":
				dest[i] = &im.Path
			case "folder":
				dest[i] = &im.Folder
			case "size":
				dest[i] = &im.Size
			case "mtime":
				dest[i] = &im.Mtime
			case "local_json":
				dest[i] = jsonText{&im.LocalJSON}
			case "batch_id":
				dest[i] = &im.BatchID
			case "vlm_json":
				dest[i] = jsonText{&im.VLMJSON}
			case "vlm_usage":
				dest[i] = jsonText{&im.VLMUsage}
			case "vlm_skip":
				dest[i] = &im.VLMSkip
			case "override_json":
				dest[i] = jsonText{&im.OverrideJSON}
			case "error":
				dest[i] = &im.Error
			case "local_at":
				dest[i] = &im.LocalAt
			case "vlm_at":
				dest[i] = &im.VLMAt
			case "lr_json":
				dest[i] = jsonText{&im.LRJSON}
			case "truth_json":
				dest[i] = jsonText{&im.TruthJSON}
			default:
				var v any
				if extra != nil {
					v = extra(c)
				}
				if v == nil {
					v = new(any)
				}
				dest[i] = v
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, rows.Err()
}

// AddPaths registers new image files; ones already tracked are skipped without a stat (a rescan of a big shoot on
// a network mount is mostly those). Returns how many were added.
func (d *DB) AddPaths(paths []string) (int, error) {
	known := map[string]bool{}
	rows, err := d.Query("SELECT path FROM images")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		known[p] = true
	}
	rows.Close()
	n := 0
	err = d.Write(func(tx *Tx) error {
		for _, p := range paths {
			if known[p] {
				continue
			}
			st, err := os.Stat(p)
			if err != nil {
				continue
			}
			mtime := float64(st.ModTime().UnixNano()) / 1e9
			k, err := affected(tx.Exec("INSERT INTO images(path, folder, size, mtime) VALUES(?,?,?,?) ON CONFLICT DO NOTHING",
				p, filepath.Dir(p), st.Size(), mtime))
			if err != nil {
				return err
			}
			n += int(k)
		}
		return nil
	})
	return n, err
}

// Rows are the images matching where (with args), in order, optionally paged; cols defaults to all.
func (d *DB) Rows(where string, args []any, order string, limit, offset int, cols string) ([]Image, error) {
	if where == "" {
		where = "1=1"
	}
	if order == "" {
		order = d.D.Collate("path")
	}
	if cols == "" {
		cols = ImageCols
	}
	q := fmt.Sprintf("SELECT %s FROM images WHERE %s ORDER BY %s", cols, where, order)
	if limit >= 0 {
		q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return ScanImages(rows, nil)
}

// RowsUnder are the images whose path is one of paths or lies under one of them, AND where, ordered by path.
//
// Files go in chunked IN lists and folders in chunked prefix matches, so a job over thousands of selected files never
// builds an expression past SQLite's depth limit (1000). A prefix match with substr() rather than LIKE keeps '_' and
// '%' in folder names literal.
func (d *DB) RowsUnder(paths []string, where string, args []any, cols string) ([]Image, error) {
	const chunk = 400
	if where == "" {
		where = "1=1"
	}
	var files, dirs []string
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			dirs = append(dirs, strings.TrimRight(p, "/")+"/")
		} else {
			files = append(files, p)
		}
	}
	seen := map[int64]Image{}
	for i := 0; i < len(files); i += chunk {
		part := files[i:min(i+chunk, len(files))]
		q := fmt.Sprintf("path IN (%s) AND (%s)", strings.TrimSuffix(strings.Repeat("?,", len(part)), ","), where)
		a := make([]any, 0, len(part)+len(args))
		for _, f := range part {
			a = append(a, f)
		}
		rows, err := d.Rows(q, append(a, args...), "", -1, 0, cols)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			seen[r.ID] = r
		}
	}
	for i := 0; i < len(dirs); i += chunk / 4 {
		part := dirs[i:min(i+chunk/4, len(dirs))]
		var ors []string
		var a []any
		for _, dir := range part {
			ors = append(ors, "substr(path, 1, CAST(? AS INTEGER)) = ?")
			a = append(a, utf8.RuneCountInString(dir), dir)
		}
		rows, err := d.Rows("("+strings.Join(ors, " OR ")+") AND ("+where+")", append(a, args...), "", -1, 0, cols)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			seen[r.ID] = r
		}
	}
	out := make([]Image, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Image) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// Row is one image by id, or ErrNotFound.
func (d *DB) Row(id int64) (Image, error) {
	rows, err := d.Rows("id = ?", []any{id}, "id", -1, 0, "")
	if err != nil {
		return Image{}, err
	}
	if len(rows) == 0 {
		return Image{}, ErrNotFound
	}
	return rows[0], nil
}

// Count counts the images matching where.
func (d *DB) Count(where string, args ...any) (int, error) {
	if where == "" {
		where = "1=1"
	}
	var n int
	err := d.QueryRow("SELECT COUNT(*) FROM images WHERE "+where, args...).Scan(&n)
	return n, err
}

// SetLocal stores a local-stage result (or clears it, with an error).
func (d *DB) SetLocal(id int64, data *string, errMsg *string) error {
	var at any
	if data != nil {
		at = Now()
	}
	_, err := d.Exec("UPDATE images SET local_json=?, error=?, local_at=?, vlm_skip=NULL WHERE id=?", data, errMsg, at, id)
	return err
}

// LocalUpdate is one image's new local result.
type LocalUpdate struct {
	ID   int64
	JSON string
}

// SetLocalMany is SetLocal for many images in one transaction.
func (d *DB) SetLocalMany(items []LocalUpdate) error {
	now := Now()
	return d.Write(func(tx *Tx) error {
		for _, it := range items {
			if _, err := tx.Exec("UPDATE images SET local_json=?, error=NULL, local_at=?, vlm_skip=NULL WHERE id=?", it.JSON, now, it.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetVLM stores the model's result, stamped with the exposure lift of the frame it was sent (see Dialect.VLMStale).
func (d *DB) SetVLM(id int64, data, usage, errMsg *string) error {
	var at any
	if data != nil {
		at = Now()
	}
	_, err := d.Exec("UPDATE images SET vlm_json="+d.D.SetJ("seen_ev", d.D.EV())+", vlm_usage=?, error=?, vlm_at=?, vlm_skip=NULL WHERE id=?",
		data, usage, errMsg, at, id)
	return err
}

// SetVLMSkip marks images a job deliberately left for the vision model, so they read as skipped rather than pending.
func (d *DB) SetVLMSkip(ids []int64, reason string) error {
	return d.Write(func(tx *Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec("UPDATE images SET vlm_skip=? WHERE id=?", reason, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// JSONUpdate sets one JSON column of one image (nil = NULL).
type JSONUpdate struct {
	ID   int64
	JSON *string
}

func (d *DB) setJSONMany(col string, items []JSONUpdate) error {
	return d.Write(func(tx *Tx) error {
		for _, it := range items {
			if _, err := tx.Exec("UPDATE images SET "+col+"=? WHERE id=?", it.JSON, it.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetLRMany stores Lightroom sidecar verdicts; an image without one should get "{}" so it isn't looked up again.
func (d *DB) SetLRMany(items []JSONUpdate) error { return d.setJSONMany("lr_json", items) }

// SetTruthMany stores imported ground truth.
func (d *DB) SetTruthMany(items []JSONUpdate) error { return d.setJSONMany("truth_json", items) }

// SetOverride stores the UI's manual corrections for one image.
func (d *DB) SetOverride(id int64, data *string) error {
	return d.setJSONMany("override_json", []JSONUpdate{{id, data}})
}

// ClearTruth drops all imported ground truth and says how many images had some.
func (d *DB) ClearTruth() (int, error) {
	n, err := affected(d.Exec("UPDATE images SET truth_json=NULL WHERE truth_json IS NOT NULL"))
	return int(n), err
}

// FolderStat counts one folder's images.
type FolderStat struct {
	Folder                     string
	N, LocalDone, VLMDone, Err int
}

// FolderStats counts per folder, for every folder under prefix (recursive).
func (d *DB) FolderStats(prefix string) (map[string]FolderStat, error) {
	where, args := d.D.UnderFolder(prefix, "folder")
	rows, err := d.Query("SELECT folder, COUNT(*), COUNT(*) FILTER (WHERE local_json IS NOT NULL), "+
		"COUNT(*) FILTER (WHERE vlm_json IS NOT NULL), COUNT(*) FILTER (WHERE error IS NOT NULL) FROM images WHERE "+where+
		" GROUP BY folder", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FolderStat{}
	for rows.Next() {
		var s FolderStat
		if err := rows.Scan(&s.Folder, &s.N, &s.LocalDone, &s.VLMDone, &s.Err); err != nil {
			return nil, err
		}
		out[s.Folder] = s
	}
	return out, rows.Err()
}

// ---- batches (cloud backends) --------------------------------------------------------------------------------------

// Batch is a submitted cloud batch.
type Batch struct {
	ID, Backend, Model string
	N                  int
	Created            float64
	State              string
	Fetched            bool
}

// SetBatch records which batch images were submitted in.
func (d *DB) SetBatch(ids []int64, batchID string) error {
	return d.Write(func(tx *Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec("UPDATE images SET batch_id=? WHERE id=?", batchID, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// AddBatch records a submitted batch.
func (d *DB) AddBatch(id, backend, model string, n int, created float64) error {
	_, err := d.Exec("INSERT INTO batches(id, backend, model, n, created, state, fetched) VALUES(?,?,?,?,?,?,0) "+
		"ON CONFLICT(id) DO UPDATE SET backend=excluded.backend, model=excluded.model, n=excluded.n, created=excluded.created, "+
		"state=excluded.state, fetched=0", id, backend, model, n, created, "submitted")
	return err
}

// Batches lists batches, oldest first.
func (d *DB) Batches(onlyUnfetched bool) ([]Batch, error) {
	q := "SELECT id, backend, model, n, created, state, fetched FROM batches"
	if onlyUnfetched {
		q += " WHERE fetched=0"
	}
	rows, err := d.Query(q + " ORDER BY created")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Batch
	for rows.Next() {
		var b Batch
		var fetched int
		var backend, model, state sql.NullString
		var n sql.NullInt64
		var created sql.NullFloat64
		if err := rows.Scan(&b.ID, &backend, &model, &n, &created, &state, &fetched); err != nil {
			return nil, err
		}
		b.Backend, b.Model, b.N, b.Created, b.State, b.Fetched = backend.String, model.String, int(n.Int64), created.Float64, state.String, fetched != 0
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetBatchState updates a batch's state and fetched flag.
func (d *DB) SetBatchState(id, state string, fetched bool) error {
	f := 0
	if fetched {
		f = 1
	}
	_, err := d.Exec("UPDATE batches SET state=?, fetched=? WHERE id=?", state, f, id)
	return err
}

// ClearBatch releases a batch's untagged images for resubmission (with erroredToo=false, only ones with no error).
func (d *DB) ClearBatch(id string, erroredToo bool) error {
	q := "UPDATE images SET batch_id=NULL WHERE batch_id=? AND " + d.D.VLMTodo()
	if !erroredToo {
		q += " AND error IS NULL"
	}
	_, err := d.Exec(q, id)
	return err
}

// DeleteImages forgets images: their rows and their job items. The files on disk are untouched, and a later job over
// their folder registers them again. Returns how many rows went.
func (d *DB) DeleteImages(ids []int64) (int, error) {
	const chunk = 400
	n := 0
	err := d.Write(func(tx *Tx) error {
		for i := 0; i < len(ids); i += chunk {
			part := ids[i:min(i+chunk, len(ids))]
			in := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
			a := make([]any, len(part))
			for j, id := range part {
				a[j] = id
			}
			if _, err := tx.Exec("DELETE FROM job_items WHERE image_id IN ("+in+")", a...); err != nil {
				return err
			}
			k, err := affected(tx.Exec("DELETE FROM images WHERE id IN ("+in+")", a...))
			if err != nil {
				return err
			}
			n += int(k)
		}
		return nil
	})
	return n, err
}
