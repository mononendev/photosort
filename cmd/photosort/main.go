// Command photosort culls and tags event photos: local focus scoring, vision-model tagging, and a web UI to check
// the work. `photosort web` is the server the UI talks to; the other commands run the same stages from a terminal.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mononendev/photosort/internal/analyzer"
	"github.com/mononendev/photosort/internal/config"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/pj"
)

// Set at build time (-ldflags "-X main.version=...").
var version = "dev"

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var workdir string

// app is what most commands need: the workdir's config and database.
type app struct {
	workdir string
	cfg     *config.Store
	db      *db.DB
}

func openApp() (*app, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	store, err := config.NewStore(workdir)
	if err != nil {
		return nil, err
	}
	d, err := db.Open(db.URL(workdir))
	if err != nil {
		return nil, fmt.Errorf("opening the database: %w", err)
	}
	return &app{workdir: workdir, cfg: store, db: d}, nil
}

func (a *app) cacheDir() string { return filepath.Join(a.workdir, "cache") }

func (a *app) close() { a.db.Close() }

// modelsDir is where weights live ($PHOTOSORT_MODELS, else the current directory, as the analyzer resolves it).
func modelsDir() string {
	if d := os.Getenv("PHOTOSORT_MODELS"); d != "" {
		return d
	}
	d, _ := os.Getwd()
	return d
}

// connectAnalyzer reaches the pixel stage: $PHOTOSORT_ANALYZER when set (the sidecar in the cluster), else a child
// process started from the analyzer/ project ($PHOTOSORT_ANALYZER_DIR, or ./analyzer).
func connectAnalyzer(ctx context.Context) (*analyzer.Client, func(), error) {
	if u := os.Getenv("PHOTOSORT_ANALYZER"); u != "" {
		return analyzer.New(u), func() {}, nil
	}
	dir := envOr("PHOTOSORT_ANALYZER_DIR", "analyzer")
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(dir); err != nil {
			// a built binary in bin/ next to the analyzer project
			if alt := filepath.Join(filepath.Dir(exe), "..", "analyzer"); fileExists(alt) {
				dir = alt
			}
		}
	}
	abs, _ := filepath.Abs(dir)
	if !fileExists(filepath.Join(abs, "pyproject.toml")) {
		return nil, nil, fmt.Errorf("no analyzer: set PHOTOSORT_ANALYZER to its URL, or PHOTOSORT_ANALYZER_DIR to the analyzer/ project")
	}
	slog.Info("starting the analyzer", "dir", abs)
	return analyzer.Spawn(ctx, abs, []string{"PHOTOSORT_MODELS=" + modelsDir()})
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	root := &cobra.Command{
		Use:           "photosort",
		Short:         "Cull and tag event photos: local focus scoring + vision-model tagging",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&workdir, "workdir", envOr("PHOTOSORT_WORKDIR", "photosort_work"),
		"state, cache and batch files (default ./photosort_work or $PHOTOSORT_WORKDIR)")
	root.AddCommand(webCmd(), scanCmd(), localCmd(), calibrateCmd(), rescoreCmd(), estimateCmd(), submitCmd(), pollCmd(),
		sortCmd(), statusCmd(), dbCmd(), modelsCmd())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// focusTiers is the local tier histogram the stage commands print.
func (a *app) printLocalSummary() error {
	tiers := pj.Obj{"tier0": 0, "tier1": 0, "tier2": 0, "tier3": 0}
	rows, err := a.db.Query("SELECT " + a.db.D.JNum("local_json", "local_tier") + " t, COUNT(*) FROM images WHERE local_json IS NOT NULL GROUP BY t")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t *float64
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return err
		}
		if t != nil {
			tiers[fmt.Sprintf("tier%d", int(*t))] = n
		}
	}
	fmt.Printf("local focus tiers: {'tier0': %v, 'tier1': %v, 'tier2': %v, 'tier3': %v}\n",
		tiers["tier0"], tiers["tier1"], tiers["tier2"], tiers["tier3"])
	return rows.Err()
}
