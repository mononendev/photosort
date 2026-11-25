package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/dhash"
	"github.com/mononendev/photosort/internal/exif"
	"github.com/mononendev/photosort/internal/export"
	"github.com/mononendev/photosort/internal/pj"
)

const (
	publishBatch       = 500
	publishSyncTimeout = 10 * time.Minute
)

// publishPollEvery is how often publish asks the gallery for the sync state. A variable so the test can shorten it.
var publishPollEvery = 2 * time.Second

// sourceWire is one photo as the gallery's admin API takes it (POST /admin-api/events/{slug}/sources). Every field
// but source_id and rel_path is null when photosort has no value.
type sourceWire struct {
	SourceID     int64          `json:"source_id"`
	RelPath      string         `json:"rel_path"`
	Taken        *string        `json:"taken"`
	TakenMS      *int           `json:"taken_ms"`
	DHash        *string        `json:"dhash"`
	Rating       *int           `json:"rating"`
	FocusTier    *int           `json:"focus_tier"`
	QualityScore *int           `json:"quality_score"`
	Keeper       *bool          `json:"keeper"`
	Subject      *string        `json:"subject"`
	Composition  *string        `json:"composition"`
	Action       *string        `json:"action"`
	Description  *string        `json:"description"`
	Keywords     []string       `json:"keywords"`
	Adjectives   []string       `json:"adjectives"`
	EXIF         map[string]any `json:"exif"`
}

func publishCmd() *cobra.Command {
	var event, folder, gallery string
	var noSync bool
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "send analyzed photos to the gallery and start its album sync",
		Long: "Sends the ratings, focus tiers, descriptions, keywords and EXIF of the analyzed photos under --folder to the\n" +
			"gallery's admin API, then asks it to match them with the event's Google Photos albums.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			photos := os.Getenv("PHOTOSORT_PHOTOS")
			if !filepath.IsAbs(folder) {
				if photos == "" {
					return fmt.Errorf("--folder is relative and PHOTOSORT_PHOTOS is not set")
				}
				folder = filepath.Join(photos, folder)
			}
			if gallery == "" {
				return fmt.Errorf("set --gallery or PHOTOSORT_GALLERY")
			}
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			rows, err := a.db.RowsUnder([]string{folder}, "local_json IS NOT NULL", nil, "")
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return fmt.Errorf("no analyzed photos under %s", folder)
			}
			source := pj.Str(a.cfg.Get()["focus_source"])
			g := &galleryClient{base: strings.TrimRight(gallery, "/") + "/admin-api/events/" + url.PathEscape(event), http: &http.Client{Timeout: 2 * time.Minute}}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			for i := 0; i < len(rows); i += publishBatch {
				batch := make([]sourceWire, 0, publishBatch)
				for _, r := range rows[i:min(i+publishBatch, len(rows))] {
					w, err := wireRow(r, source, photos, a.cacheDir())
					if err != nil {
						return err
					}
					batch = append(batch, w)
				}
				if err := g.do(ctx, http.MethodPost, "/sources", map[string]any{"photos": batch}, nil); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "sent %d photos to %s\n", len(rows), event)
			if noSync {
				return nil
			}
			return g.sync(ctx, event, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&event, "event", "", "gallery event slug")
	f.StringVar(&folder, "folder", "", "folder of the analyzed photos (relative to $PHOTOSORT_PHOTOS)")
	f.StringVar(&gallery, "gallery", os.Getenv("PHOTOSORT_GALLERY"), "gallery admin API base URL (default $PHOTOSORT_GALLERY)")
	f.BoolVar(&noSync, "no-sync", false, "send the photos but do not start the album sync")
	_ = cmd.MarkFlagRequired("event")
	_ = cmd.MarkFlagRequired("folder")
	return cmd
}

// wireRow builds the gallery's record of one image.
func wireRow(r db.Image, source, photos, cacheDir string) (sourceWire, error) {
	rec, err := export.FinalRecord(exportRow(r), source)
	if err != nil {
		return sourceWire{}, fmt.Errorf("%s: %w", r.Path, err)
	}
	var local struct {
		EXIF map[string]any `json:"exif"`
	}
	if err := json.Unmarshal([]byte(*r.LocalJSON), &local); err != nil {
		return sourceWire{}, fmt.Errorf("%s: local_json: %w", r.Path, err)
	}
	w := sourceWire{
		SourceID:     r.ID,
		RelPath:      relPath(r.Path, photos),
		Taken:        wireStr(local.EXIF["taken"]),
		TakenMS:      parseSubSecMS(exif.SubSecTimeOriginal(r.Path)),
		DHash:        thumbHash(filepath.Join(cacheDir, strconv.FormatInt(r.ID, 10)+"_thumb.jpg")),
		Rating:       wireInt(rec.Rating),
		FocusTier:    wireInt(rec.FocusTier),
		QualityScore: wireInt(rec.QualityScore),
		Subject:      wireKnown(rec.Subject),
		Composition:  wireKnown(rec.Composition),
		Action:       wireStr(rec.Action),
		Description:  wireStr(rec.Description),
		Keywords:     wireStrings(rec.Keywords),
		Adjectives:   wireStrings(rec.Adjectives),
		EXIF:         map[string]any{},
	}
	if k, ok := rec.Keeper.(bool); ok {
		w.Keeper = &k
	}
	for _, k := range []string{"camera", "lens", "f_number", "shutter_s", "iso", "focal_mm"} {
		if v := local.EXIF[k]; v != nil {
			w.EXIF[k] = v
		}
	}
	return w, nil
}

// relPath is p relative to the photos root when it lies under it, else p.
func relPath(p, root string) string {
	if root == "" {
		return p
	}
	if rel, ok := strings.CutPrefix(p, strings.TrimRight(root, "/")+"/"); ok {
		return rel
	}
	return p
}

// parseSubSecMS turns EXIF SubSecTimeOriginal, the digits after the decimal point of the capture second, into
// milliseconds: "94" is .94 s, 940 ms. Nil when there are no digits.
func parseSubSecMS(s string) *int {
	s = strings.TrimSpace(s)
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 {
		return nil
	}
	ms, _ := strconv.Atoi((s[:n] + "00")[:3])
	return &ms
}

// thumbHash is the dHash of a cached thumbnail, nil when the file is missing or not a JPEG.
func thumbHash(path string) *string {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	img, err := jpeg.Decode(fh)
	if err != nil {
		return nil
	}
	h := dhash.Hex(dhash.Hash(img))
	return &h
}

func wireStr(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// wireKnown is wireStr, but "unknown" (what FinalRecord fills in without a model result) is null.
func wireKnown(v any) *string {
	if s := wireStr(v); s != nil && *s != "unknown" {
		return s
	}
	return nil
}

// wireInt takes integers and integral floats; anything else is null.
func wireInt(v any) *int {
	switch x := v.(type) {
	case int64:
		n := int(x)
		return &n
	case int:
		return &x
	case float64:
		if x == float64(int(x)) {
			n := int(x)
			return &n
		}
	}
	return nil
}

func wireStrings(v any) []string {
	out := []string{}
	if l, ok := v.([]any); ok {
		for _, e := range l {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

type galleryClient struct {
	base string // .../admin-api/events/<slug>
	http *http.Client
}

// do sends a JSON request and decodes a 2xx body into out (when not nil). A failure reads gallery: HTTP <code>: <detail>.
func (g *galleryClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("gallery: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("gallery: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Detail any `json:"detail"`
		}
		detail := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Detail != nil {
			if s, ok := e.Detail.(string); ok {
				detail = s
			} else {
				b, _ := json.Marshal(e.Detail)
				detail = string(b)
			}
		}
		return fmt.Errorf("gallery: HTTP %d: %s", resp.StatusCode, detail)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("gallery: %w", err)
		}
	}
	return nil
}

// sync starts the album sync and waits for it, up to ten minutes.
func (g *galleryClient) sync(ctx context.Context, slug string, w io.Writer) error {
	if err := g.do(ctx, http.MethodPost, "/sync", nil, nil); err != nil {
		return err
	}
	deadline := time.Now().Add(publishSyncTimeout)
	for {
		var st struct {
			State  string  `json:"sync_state"`
			Error  *string `json:"sync_error"`
			Result struct {
				Items           int `json:"items"`
				MatchedTime     int `json:"matched_time"`
				MatchedTimeHash int `json:"matched_time_hash"`
				MatchedHash     int `json:"matched_hash"`
				Unmatched       int `json:"unmatched"`
			} `json:"sync_result"`
		}
		if err := g.do(ctx, http.MethodGet, "/sync", nil, &st); err != nil {
			return err
		}
		switch st.State {
		case "failed":
			msg := ""
			if st.Error != nil {
				msg = *st.Error
			}
			return fmt.Errorf("gallery sync failed: %s", msg)
		case "done":
			r := st.Result
			fmt.Fprintf(w, "synced %s: %d album photos, %d matched by time, %d by time+hash, %d by hash, %d unmatched\n",
				slug, r.Items, r.MatchedTime, r.MatchedTimeHash, r.MatchedHash, r.Unmatched)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gallery sync did not finish within %s", publishSyncTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(publishPollEvery):
		}
	}
}
