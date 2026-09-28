package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/mononendev/photosort/internal/pj"
)

// untrackIn narrows an untrack to these paths (files, or folders and everything under them), on top of the filters.
type untrackIn struct {
	Paths []string `json:"paths"`
}

// untrack forgets the images matching the Photos filters in the query (and under the body's paths, if any): their rows,
// job items and cached renders go; the files stay. Photos you rated, edited or have ground truth for are kept.
// ?dry_run=1 only counts. Refused while a job is queued or running, since it would register them again or write to
// rows that are gone.
func (s *Server) untrack(r *http.Request) (any, error) {
	w, args, err := s.imageFilter(r)
	if err != nil {
		return nil, err
	}
	dry, err := qBool(r, "dry_run")
	if err != nil {
		return nil, err
	}
	var in untrackIn
	if r.ContentLength != 0 {
		if err := decodeBody(r, &in); err != nil {
			return nil, err
		}
	}
	paths := make([]string, len(in.Paths))
	for i, p := range in.Paths {
		if paths[i], err = s.safePath(p); err != nil {
			return nil, err
		}
	}

	d := s.DB.D
	keep := "((override_json IS NOT NULL AND NOT " + d.EmptyObject("override_json") + ") OR truth_json IS NOT NULL)"
	var ids []int64
	kept := 0
	if len(paths) > 0 {
		rows, err := s.DB.RowsUnder(paths, w, args, "id, override_json, truth_json")
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if (row.OverrideJSON != nil && len(pj.Parse(row.OverrideJSON)) > 0) || row.TruthJSON != nil {
				kept++
			} else {
				ids = append(ids, row.ID)
			}
		}
	} else {
		rows, err := s.DB.Rows("("+w+") AND NOT "+keep, args, "id", -1, 0, "id")
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		if kept, err = s.DB.Count("("+w+") AND "+keep, args...); err != nil {
			return nil, err
		}
	}
	out := pj.Obj{"untracked": len(ids), "kept": kept}
	if dry != nil && *dry {
		return out, nil
	}
	if busy, err := s.DB.BusyJob(); err != nil {
		return nil, err
	} else if busy != 0 {
		return nil, errf(409, "job #%d is queued or running; cancel it (or let it finish) before untracking", busy)
	}
	n, err := s.DB.DeleteImages(ids)
	if err != nil {
		return nil, err
	}
	// An id can come back for a new image, so its cached renders must not outlive it.
	for _, id := range ids {
		for _, sfx := range []string{"", "_thumb", "_crop", "_full"} {
			os.Remove(filepath.Join(s.CacheDir, itoa(id)+sfx+".jpg"))
		}
	}
	out["untracked"] = n
	return out, nil
}
