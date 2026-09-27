package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/mononendev/photosort/internal/api"
	"github.com/mononendev/photosort/internal/backends"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/jobs"
	"github.com/mononendev/photosort/internal/local"
	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/sidecar"
)

// vlmAdapter makes a backends.Backend what the job runner calls.
type vlmAdapter struct {
	backends.Backend
	cacheDir string
}

func (v vlmAdapter) Classify(ctx context.Context, img db.Image, model string, cfg pj.Obj) (jobs.Result, error) {
	if img.LocalJSON == nil {
		return jobs.Result{}, fmt.Errorf("image %d has no local result", img.ID)
	}
	item, err := backends.LoadItem(v.cacheDir, img.ID, *img.LocalJSON)
	if err != nil {
		return jobs.Result{}, err
	}
	r := v.Backend.Classify(ctx, item, model, cfg)
	return jobs.Result{Key: r.Key, Data: r.Data, Usage: r.Usage, Error: r.Error}, nil
}

// ingestSidecars reads Lightroom sidecars for rows that have none recorded yet and stores what it finds.
func ingestSidecars(d *db.DB, rows []db.Image) (int, error) {
	in := make([]sidecar.Row, len(rows))
	for i, r := range rows {
		in[i] = sidecar.Row{ID: r.ID, Path: r.Path}
	}
	return sidecar.Ingest(in, func(items []sidecar.Item) error {
		ups := make([]db.JSONUpdate, len(items))
		for i, it := range items {
			j := it.LR.JSON()
			ups[i] = db.JSONUpdate{ID: it.ID, JSON: &j}
		}
		return d.SetLRMany(ups)
	})
}

// localStage binds the analyzer and the file readers into the runner's per-image local stage.
func localStage(px local.Pixels, cacheDir string) func(ctx context.Context, cfg pj.Obj, id int64, path string) (pj.Obj, error) {
	meta := local.FileMeta()
	return func(ctx context.Context, cfg pj.Obj, id int64, path string) (pj.Obj, error) {
		return local.Analyze(ctx, px, meta, cfg, id, path, cacheDir)
	}
}

func webCmd() *cobra.Command {
	var photos, host string
	var port int
	cmd := &cobra.Command{
		Use:   "web",
		Short: "run the API server and the job worker",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			if h := os.Getenv("OLLAMA_HOST"); h != "" { // deployment wiring wins over a stale config.json
				a.cfg.Override(pj.Obj{"base_url": h, "backend": "ollama"})
			}
			root, err := filepath.Abs(photos)
			if err != nil {
				return err
			}
			if r, err := filepath.EvalSymlinks(root); err == nil {
				root = r
			}
			an, stopAn, err := connectAnalyzer(ctx)
			if err != nil {
				slog.Warn("no analyzer; the local stage and the full-size viewer won't work", "err", err)
			} else {
				defer stopAn()
			}
			cache := a.cacheDir()
			srv := &api.Server{DB: a.db, Config: a.cfg, Analyzer: an, Workdir: a.workdir, PhotosRoot: root, CacheDir: cache,
				Version: version, ModelsDir: modelsDir(), WebDist: webDist()}
			go srv.WatchAnalyzer(ctx)
			jobs.Device = func() any { return pj.Get(srv.AnalyzerHealth(), "device") }

			deps := jobs.Deps{DB: a.db, Config: a.cfg.Get, PhotosRoot: root, CacheDir: cache,
				Backend: func(name, baseURL string) (jobs.VLM, error) {
					b, err := backends.Get(name, baseURL)
					if err != nil {
						return nil, err
					}
					return vlmAdapter{b, cache}, nil
				},
				Ingest: func(rows []db.Image) error { _, err := ingestSidecars(a.db, rows); return err },
			}
			if an != nil {
				deps.Local = localStage(an, cache)
			} else {
				deps.Local = func(context.Context, pj.Obj, int64, string) (pj.Obj, error) {
					return nil, errors.New("the analyzer is not running")
				}
			}
			runner := jobs.New(deps)
			go runner.Run()

			hs := &http.Server{Addr: net.JoinHostPort(host, fmt.Sprint(port)), Handler: logRequests(srv.Handler()),
				ReadHeaderTimeout: 30 * time.Second}
			errc := make(chan error, 1)
			go func() { errc <- hs.ListenAndServe() }()
			slog.Info("photosort listening", "addr", hs.Addr, "photos", root, "workdir", a.workdir, "database", a.db.Kind())
			select {
			case err := <-errc:
				return err
			case <-ctx.Done():
			}
			// SIGTERM in a rolling restart: stop taking requests, give in-flight images the drain window, and hand
			// the jobs back so the next server resumes them at once.
			slog.Info("shutting down")
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			hs.Shutdown(sctx)
			runner.Shutdown(jobs.Drain)
			return nil
		},
	}
	cmd.Flags().StringVar(&photos, "photos", envOr("PHOTOSORT_PHOTOS", "."), "photos root (read-only is fine)")
	cmd.Flags().StringVar(&host, "host", "0.0.0.0", "listen address")
	cmd.Flags().IntVar(&port, "port", envInt("PORT", 8080), "listen port")
	return cmd
}

func envInt(k string, def int) int {
	var v int
	if _, err := fmt.Sscan(os.Getenv(k), &v); err == nil {
		return v
	}
	return def
}

// webDist is the built UI to serve for local dev: $PHOTOSORT_WEB_DIST, else web/dist when present.
func webDist() string {
	if d := os.Getenv("PHOTOSORT_WEB_DIST"); d != "" {
		return d
	}
	if fileExists(filepath.Join("web", "dist", "index.html")) {
		return filepath.Join("web", "dist")
	}
	return ""
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		h.ServeHTTP(sw, r)
		if sw.code >= 500 || time.Since(t) > 5*time.Second {
			slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.code, "dur", time.Since(t))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
