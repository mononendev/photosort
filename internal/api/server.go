// Package api is photosort's HTTP API: the JSON under /api and the cached JPEGs under /media that the React UI
// (web/) reads. Response shapes are the Python backend's, key for key, so the UI didn't change.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mononendev/photosort/internal/analyzer"
	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/pj"
)

// Server holds what the handlers need.
type Server struct {
	DB         *db.DB
	Config     *config.Store
	Analyzer   *analyzer.Pool // nil: pixel endpoints answer 503
	Workdir    string
	PhotosRoot string
	CacheDir   string
	Version    string
	ModelsDir  string
	WebDist    string // serve the built UI from here when set (local dev; nginx does it in production)
	Log        *slog.Logger

	debugMu    sync.Mutex
	debugCache []debugEntry // focus-debug results for a few recent images
	device     sync.Map     // analyzer health, refreshed in the background
}

// HTTPError is an error with a status; the body is FastAPI's {"detail": ...}.
type HTTPError struct {
	Code   int
	Detail any
}

func (e *HTTPError) Error() string { return fmt.Sprint(e.Detail) }

func errf(code int, format string, a ...any) error {
	return &HTTPError{code, fmt.Sprintf(format, a...)}
}

var errNotFound = &HTTPError{404, "Not Found"}

type handler func(r *http.Request) (any, error)

func (s *Server) wrap(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h(r)
		if err != nil {
			var he *HTTPError
			if !errors.As(err, &he) {
				s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
				he = &HTTPError{500, err.Error()}
			}
			writeJSON(w, he.Code, map[string]any{"detail": he.Detail})
			return
		}
		if v == nil {
			v = json.RawMessage("null")
		}
		writeJSON(w, 200, v)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// Handler is every route.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	m := http.NewServeMux()
	route := func(pattern string, h handler) { m.HandleFunc(pattern, s.wrap(h)) }

	route("GET /api/health", s.health)
	route("GET /api/stats", s.stats)
	route("GET /api/tree", s.tree)

	route("POST /api/jobs", s.createJob)
	route("GET /api/jobs", s.listJobs)
	route("GET /api/jobs/{id}", s.getJob)
	route("GET /api/jobs/{id}/detail", s.jobDetail)
	route("GET /api/jobs/{id}/items", s.jobItems)
	route("POST /api/jobs/{id}/cancel", s.cancelJob)
	route("POST /api/jobs/{id}/override", s.overrideJob)

	route("GET /api/images", s.listImages)
	route("GET /api/images/facets", s.facets)
	route("POST /api/images/untrack", s.untrack)
	route("GET /api/images/{id}", s.getImage)
	route("PATCH /api/images/{id}", s.patchImage)
	route("GET /api/images/{id}/trace", s.trace)
	route("GET /api/images/{id}/vlm-request", s.vlmRequest)
	route("GET /api/images/{id}/focus-debug", s.focusDebug)
	route("POST /api/images/{id}/detect", s.detect)

	m.HandleFunc("GET /media/thumb/{id}", s.media("_thumb"))
	m.HandleFunc("GET /media/frame/{id}", s.media(""))
	m.HandleFunc("GET /media/crop/{id}", s.media("_crop"))
	m.HandleFunc("GET /media/full/{id}", s.full)

	route("GET /api/config", s.getConfig)
	route("GET /api/config/defaults", func(*http.Request) (any, error) { return config.Defaults(), nil })
	route("PUT /api/config", s.putConfig)
	route("POST /api/rescore", s.rescore)
	route("GET /api/config/history", s.history)
	route("POST /api/config/history/{id}/restore", s.restore)
	route("GET /api/calibration", s.calibration)
	route("GET /api/models", s.models)

	route("GET /api/truth", s.getTruth)
	route("POST /api/truth/upload", s.truthUpload)
	route("POST /api/truth/import", s.truthImport)
	route("DELETE /api/truth", s.truthClear)

	route("POST /api/export", s.export)
	route("GET /api/exports", s.exports)

	route("/api/", func(*http.Request) (any, error) { return nil, errNotFound })
	if s.WebDist != "" {
		m.Handle("/", spa(s.WebDist))
	}
	return m
}

// spa serves the built UI with index.html for any path that isn't a file (nginx's try_files in production).
func spa(dist string) http.Handler {
	fs := http.FileServer(http.Dir(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	})
}

// ---- helpers ------------------------------------------------------------------------------------------------------

// rel is a path relative to the photos root ("" for the root itself), or the path unchanged when outside it.
func (s *Server) rel(p string) string {
	root := strings.TrimRight(s.PhotosRoot, "/")
	switch {
	case p == root:
		return ""
	case strings.HasPrefix(p, root+"/"):
		return p[len(root)+1:]
	}
	return p
}

// resolve is Python's Path.resolve(): symlinks followed as far as the path exists, the rest cleaned lexically.
func resolve(p string) string {
	p, _ = filepath.Abs(p)
	rest := ""
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// safePath resolves a path from the UI (relative to the photos root, or absolute) and refuses anything outside it.
func (s *Server) safePath(p string) (string, error) {
	var path string
	if filepath.IsAbs(p) {
		path = resolve(p)
	} else {
		path = resolve(filepath.Join(s.PhotosRoot, p))
	}
	root := resolve(s.PhotosRoot)
	if path != root && !strings.HasPrefix(path, strings.TrimRight(root, "/")+"/") {
		return "", errf(400, "path outside photos root")
	}
	return path, nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, &HTTPError{422, "id must be an integer"}
	}
	return id, nil
}

// Query parameters, parsed the way FastAPI does (422 on a bad value; nil when absent or empty).

func qInt(r *http.Request, k string) (*int, error) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return nil, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return nil, &HTTPError{422, fmt.Sprintf("%s: not a valid integer", k)}
	}
	return &i, nil
}

func qFloat(r *http.Request, k string) (*float64, error) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, &HTTPError{422, fmt.Sprintf("%s: not a valid number", k)}
	}
	return &f, nil
}

func qBool(r *http.Request, k string) (*bool, error) {
	v := strings.ToLower(r.URL.Query().Get(k))
	switch v {
	case "":
		return nil, nil
	case "1", "true", "t", "yes", "y", "on":
		b := true
		return &b, nil
	case "0", "false", "f", "no", "n", "off":
		b := false
		return &b, nil
	}
	return nil, &HTTPError{422, fmt.Sprintf("%s: not a valid boolean", k)}
}

func qIntDefault(r *http.Request, k string, def, max int) (int, error) {
	p, err := qInt(r, k)
	if err != nil {
		return 0, err
	}
	if p == nil {
		return def, nil
	}
	if max > 0 && *p > max {
		return 0, &HTTPError{422, fmt.Sprintf("%s: must be <= %d", k, max)}
	}
	return *p, nil
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return &HTTPError{422, "invalid body: " + err.Error()}
	}
	return nil
}

func (s *Server) cfg() pj.Obj { return s.Config.Get() }

func (s *Server) focusSource(cfg pj.Obj) string {
	return pj.Str(pj.Or(cfg["focus_source"], "vlm"))
}

// ---- health ---------------------------------------------------------------------------------------------------------

// WatchAnalyzer keeps the analyzer's health (device, installed pose models) fresh for /api/health.
func (s *Server) WatchAnalyzer(ctx context.Context) {
	if s.Analyzer == nil {
		return
	}
	for {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		h, err := s.Analyzer.Health(hctx)
		cancel()
		if err == nil {
			s.device.Store("health", h)
		} else {
			s.device.Delete("health")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// AnalyzerHealth is the last health answer, or nil when the analyzer isn't answering.
func (s *Server) AnalyzerHealth() pj.Obj {
	if v, ok := s.device.Load("health"); ok {
		return v.(pj.Obj)
	}
	return nil
}

func (s *Server) health(*http.Request) (any, error) {
	cfg := s.cfg()
	job, err := s.DB.RunningJob()
	if err != nil {
		return nil, err
	}
	var current any
	if job != 0 {
		current = job
	}
	h := s.AnalyzerHealth()
	return pj.Obj{"ok": true, "version": s.Version, "photos_root": s.PhotosRoot, "workdir": s.Workdir,
		"models_dir": s.ModelsDir, "device": pj.Get(h, "device"), "backend": cfg["backend"], "ollama": cfg["base_url"],
		"current_job": current, "database": s.DB.Kind(), "analyzer": h != nil,
		"analyzer_pods": pj.Get(h, "pods"), "analyzer_slots": pj.Get(h, "slots")}, nil
}
