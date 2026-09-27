package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/local"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/scan"
	"github.com/mononendev/photosort/internal/stats"
	"github.com/mononendev/photosort/internal/truth"
)

func scanCmd() *cobra.Command {
	var skipDupes bool
	cmd := &cobra.Command{
		Use:   "scan DIR...",
		Short: "register image files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, dirs []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			abs := make([]string, len(dirs))
			for i, d := range dirs {
				abs[i], _ = filepath.Abs(d)
			}
			paths := scan.FindImages(abs, skipDupes)
			n, err := a.db.AddPaths(paths)
			if err != nil {
				return err
			}
			rows, err := a.db.Rows("lr_json IS NULL", nil, "", -1, 0, "id, path")
			if err != nil {
				return err
			}
			m, err := ingestSidecars(a.db, rows)
			if err != nil {
				return err
			}
			total, _ := a.db.Count("")
			fmt.Printf("found %d images, %d new (%d with Lightroom sidecars); total tracked %d\n", len(paths), n, m, total)
			return nil
		},
	}
	cmd.Flags().BoolVar(&skipDupes, "skip-raw-dupes", false, "if a JPEG and RAW share a name, only use the JPEG")
	return cmd
}

func localCmd() *cobra.Command {
	var workers, limit int
	var retry bool
	var model string
	cmd := &cobra.Command{
		Use:   "local",
		Short: "stage 1: detect people, score sharpness, build frames and crops",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			where := "local_json IS NULL"
			if !retry {
				where += " AND error IS NULL"
			}
			rows, err := a.db.Rows(where, nil, "", -1, 0, "id, path")
			if err != nil {
				return err
			}
			if limit > 0 && len(rows) > limit {
				rows = rows[:limit]
			}
			if len(rows) == 0 {
				fmt.Println("nothing to do")
				return nil
			}
			cfg := a.cfg.Get()
			if workers > 0 {
				cfg["workers"] = float64(workers)
			}
			if model != "" {
				det := pj.O(cfg, "detector")
				if det == nil {
					det = pj.Obj{}
					cfg["detector"] = det
				}
				det["model"] = model
			}
			an, stop, err := connectAnalyzer(cmd.Context())
			if err != nil {
				return err
			}
			defer stop()
			n := max(1, pj.Int(cfg["workers"]))
			fmt.Printf("local stage: %d images, %d workers\n", len(rows), n)
			analyze := localStage(an, a.cacheDir())
			t0 := time.Now()
			var ok, bad, done atomic.Int64
			work := make(chan db.Image)
			var wg sync.WaitGroup
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for r := range work {
						if cmd.Context().Err() != nil {
							continue
						}
						data, err := analyze(context.Background(), cfg, r.ID, r.Path)
						if err == nil {
							s := pj.Dumps(data)
							err = a.db.SetLocal(r.ID, &s, nil)
						}
						if err != nil {
							m := "local: " + err.Error()
							a.db.SetLocal(r.ID, nil, &m)
							bad.Add(1)
						} else {
							ok.Add(1)
						}
						fmt.Fprintf(os.Stderr, "\r%d/%d", done.Add(1), len(rows))
					}
				}()
			}
			for _, r := range rows {
				work <- r
			}
			close(work)
			wg.Wait()
			dt := time.Since(t0).Seconds()
			fmt.Fprintln(os.Stderr)
			fmt.Printf("done: %d ok, %d errors, %.0fs (%.1f img/s)\n", ok.Load(), bad.Load(), dt, float64(len(rows))/dt)
			return a.printLocalSummary()
		},
	}
	cmd.Flags().IntVar(&workers, "workers", 0, "parallel images (default: config workers)")
	cmd.Flags().IntVar(&limit, "limit", 0, "only the first N")
	cmd.Flags().BoolVar(&retry, "retry-errors", false, "also redo images that failed before")
	cmd.Flags().StringVar(&model, "detector", "", "pose model for this run (default: config detect_model)")
	return cmd
}

func rescoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rescore",
		Short: "re-derive local tiers with the current thresholds (no re-detection)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			res, err := local.Rescore(a.db, local.FileMeta(), a.cfg.Get(), true)
			if err != nil {
				return err
			}
			fmt.Printf("rescored; %d images changed tier, EXIF backfilled on %d, AF points read on %d, primary re-picked on %d\n",
				res.Changed, res.ExifBackfilled, res.AFBackfilled, res.PrimaryChanged)
			if res.Errors > 0 {
				fmt.Printf("%d rows failed; first: %s\n", res.Errors, *res.FirstError)
			}
			return a.printLocalSummary()
		},
	}
}

// calibrateCmd prints the sharpness distribution and builds a contact sheet of head crops sorted by sharpness.
func calibrateCmd() *cobra.Command {
	var tiles int
	var metric string
	cmd := &cobra.Command{
		Use:   "calibrate",
		Short: "show the sharpness distribution and a contact sheet to pick focus thresholds",
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, ok := truth.Metrics[metric]
			if !ok {
				return fmt.Errorf("--metric must be eye, hf or head")
			}
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			cfg := a.cfg.Get()
			rows, err := a.db.Rows("local_json IS NOT NULL", nil, "", -1, 0, "id, path, local_json")
			if err != nil {
				return err
			}
			type val struct {
				s    float64
				id   int64
				path string
				d    pj.Obj
			}
			key := strings.TrimPrefix(m.Path, "$.")
			var vals []val
			for _, r := range rows {
				d := pj.Parse(r.LocalJSON)
				if v, ok := pj.Float(d[key]); ok {
					vals = append(vals, val{v, r.ID, r.Path, d})
				}
			}
			if len(vals) == 0 {
				fmt.Println("no local results yet")
				return nil
			}
			sort.SliceStable(vals, func(i, j int) bool {
				if vals[i].s != vals[j].s {
					return vals[i].s < vals[j].s
				}
				return vals[i].id < vals[j].id
			})
			arr := make([]float64, len(vals))
			for i, v := range vals {
				arr[i] = v.s
			}
			fmt.Printf("%d images with a primary-subject %s value. Percentiles:\n", len(vals), metric)
			for _, q := range []int{5, 10, 25, 50, 75, 90, 95} {
				fmt.Printf("  p%-3d %.4f\n", q, stats.Percentile(arr, float64(q)))
			}
			focus := pj.O(cfg, "focus")
			parts := make([]string, 3)
			for i, k := range m.Keys {
				parts[i] = fmt.Sprintf("%s = %v", k, focus[k])
			}
			fmt.Println("current thresholds:", strings.Join(parts, ", "))

			// contact sheet: N tiles evenly spaced across the sorted range
			n := min(tiles, len(vals))
			const tile, cols = 256, 8
			rowsN := (n + cols - 1) / cols
			sheet := image.NewRGBA(image.Rect(0, 0, cols*tile, rowsN*(tile+28)))
			draw.Draw(sheet, sheet.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
			for k, f := range stats.Linspace(0, float64(len(vals)-1), n) {
				v := vals[int(f)]
				im := headTile(a.cacheDir(), v.id, v.d, tile)
				x, y := (k%cols)*tile, (k/cols)*(tile+28)
				draw.Draw(sheet, image.Rect(x, y, x+im.Bounds().Dx(), y+im.Bounds().Dy()), im, im.Bounds().Min, draw.Src)
				name := filepath.Base(v.path)
				if len([]rune(name)) > 22 {
					name = string([]rune(name)[:22])
				}
				label(sheet, x+4, y+tile+16, fmt.Sprintf("%.4f t%v %s", v.s, v.d["local_tier"], name))
			}
			out := filepath.Join(a.workdir, "calibration_sheet.jpg")
			fh, err := os.Create(out)
			if err != nil {
				return err
			}
			defer fh.Close()
			if err := jpeg.Encode(fh, sheet, &jpeg.Options{Quality: 85}); err != nil {
				return err
			}
			fmt.Printf("contact sheet (sharpness ascending, left-to-right, top-to-bottom): %s\n", out)
			fmt.Println("Pick the sharpness values where a miss becomes soft, soft becomes slightly soft, and slightly soft becomes sharp,")
			fmt.Printf("then set focus.%s / focus.%s / focus.%s in %s and run `photosort rescore`.\n",
				m.Keys[2], m.Keys[1], m.Keys[0], filepath.Join(a.workdir, "config.json"))
			return nil
		},
	}
	cmd.Flags().IntVar(&tiles, "tiles", 64, "tiles on the contact sheet")
	cmd.Flags().StringVar(&metric, "metric", "eye", "eye = eye-band Laplacian, hf = eye-band FFT ratio, head = head-box Laplacian")
	return cmd
}

// headTile is the primary's head from the cached crop (or the frame), fit into size x size; gray when unreadable.
func headTile(cache string, id int64, d pj.Obj, size int) image.Image {
	gray := func() image.Image {
		im := image.NewRGBA(image.Rect(0, 0, size, size))
		draw.Draw(im, im.Bounds(), image.NewUniform(color.Gray{128}), image.Point{}, draw.Src)
		return im
	}
	cp := filepath.Join(cache, fmt.Sprintf("%d_crop.jpg", id))
	usedCrop := fileExists(cp)
	p := cp
	if !usedCrop {
		p = filepath.Join(cache, fmt.Sprintf("%d.jpg", id))
	}
	f, err := os.Open(p)
	if err != nil {
		return gray()
	}
	defer f.Close()
	im, err := jpeg.Decode(f)
	if err != nil {
		return gray()
	}
	people := pj.A(d, "people")
	if len(people) == 0 {
		return gray()
	}
	head := pj.A(people[0], "head")
	cb := pj.A(d, "crop_box")
	if usedCrop && len(cb) == 4 && len(head) == 4 { // map the head box into crop coordinates
		sx := float64(im.Bounds().Dx()) / (pj.F(cb[2]) - pj.F(cb[0]))
		hb := [4]float64{(pj.F(head[0]) - pj.F(cb[0])) * sx, (pj.F(head[1]) - pj.F(cb[1])) * sx,
			(pj.F(head[2]) - pj.F(cb[0])) * sx, (pj.F(head[3]) - pj.F(cb[1])) * sx}
		r := image.Rect(max(0, int(hb[0]-20)), max(0, int(hb[1]-20)), min(im.Bounds().Dx(), int(hb[2]+20)), min(im.Bounds().Dy(), int(hb[3]+20)))
		if sub, ok := im.(interface {
			SubImage(image.Rectangle) image.Image
		}); ok && !r.Empty() {
			im = sub.SubImage(r)
		}
	}
	b := im.Bounds()
	k := min(1, float64(size)/float64(max(b.Dx(), b.Dy())))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(b.Dx())*k)), max(1, int(float64(b.Dy())*k))))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), im, b, xdraw.Over, nil)
	return dst
}

func label(img *image.RGBA, x, y int, s string) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.White), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "pipeline status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			c := func(w string) int { n, _ := a.db.Count(w); return n }
			fmt.Printf("workdir: %s\n", a.workdir)
			fmt.Printf("database: %s\n", a.db.Kind())
			fmt.Printf("images: %d tracked, %d local done, %d in flight, %d tagged, %d errors\n", c(""), c("local_json IS NOT NULL"),
				c("batch_id IS NOT NULL AND vlm_json IS NULL"), c("vlm_json IS NOT NULL"), c("error IS NOT NULL"))
			bs, err := a.db.Batches(false)
			if err != nil {
				return err
			}
			for _, b := range bs {
				f := 0
				if b.Fetched {
					f = 1
				}
				fmt.Printf("  batch %s %s/%s n=%d state=%s fetched=%d\n", b.ID, b.Backend, b.Model, b.N, b.State, f)
			}
			if c("local_json IS NOT NULL") > 0 {
				if err := a.printLocalSummary(); err != nil {
					return err
				}
			}
			if err := a.printUsage(); err != nil {
				return err
			}
			errs, _ := a.db.Rows("error IS NOT NULL", nil, "", 5, 0, "path, error")
			for _, e := range errs {
				msg := *e.Error
				if len([]rune(msg)) > 120 {
					msg = string([]rune(msg)[:120])
				}
				fmt.Printf("  error: %s: %s\n", filepath.Base(e.Path), msg)
			}
			return nil
		},
	}
}

func (a *app) printUsage() error {
	d := a.db.D
	var n int
	var in, out float64
	err := a.db.QueryRow("SELECT COUNT(*), COALESCE(SUM("+d.JNum("vlm_usage", "in")+"), 0), COALESCE(SUM("+
		d.JNum("vlm_usage", "out")+"), 0) FROM images WHERE vlm_usage IS NOT NULL").Scan(&n, &in, &out)
	if err != nil || n == 0 {
		return err
	}
	fmt.Printf("results so far: %d images, %s input tokens, %s output tokens (avg %s in / %s out per image)\n",
		n, commas(int64(in)), commas(int64(out)), commas(int64(in)/int64(n)), commas(int64(out)/int64(n)))
	return nil
}

// commas is Python's f"{n:,}".
func commas(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
