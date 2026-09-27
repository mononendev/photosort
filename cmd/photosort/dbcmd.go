package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/export"
)

func exportRow(r db.Image) export.Row {
	return export.Row{Path: r.Path, Error: r.Error, LocalJSON: r.LocalJSON, VLMJSON: r.VLMJSON, OverrideJSON: r.OverrideJSON}
}

// tables in copy order, with their columns.
var tables = []struct{ name, cols string }{
	{"images", db.ImageCols + ", batch_id"},
	{"batches", "id, backend, model, n, created, state, fetched"},
	{"jobs", "id, created, started, finished, state, stage, paths_json, options_json, total, done, errors, message, owner, heartbeat, lane, priority, stages_json"},
	{"job_items", "id, job_id, image_id, stage, started, finished, seconds, error, usage_json"},
}

func dbCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "db", Short: "database tools"}
	var from, to string
	cp := &cobra.Command{
		Use:   "copy --from URL --to URL",
		Short: "copy everything from one database to another (e.g. the workdir's SQLite file into Postgres)",
		Long: "Copies images, batches, jobs and job items. URLs are sqlite://<path> (or a bare path) and postgres://...\n" +
			"The target must be empty. Point PHOTOSORT_DB at it afterwards.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" || to == "" {
				return fmt.Errorf("--from and --to are required")
			}
			src, err := db.Open(from)
			if err != nil {
				return fmt.Errorf("source: %w", err)
			}
			defer src.Close()
			dst, err := db.Open(to)
			if err != nil {
				return fmt.Errorf("target: %w", err)
			}
			defer dst.Close()
			for _, t := range tables {
				var n int
				if err := dst.QueryRow("SELECT COUNT(*) FROM " + t.name).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					return fmt.Errorf("the target already has %d rows in %s; copy only into an empty database", n, t.name)
				}
			}
			for _, t := range tables {
				n, err := copyTable(src, dst, t.name, dedupCols(t.cols))
				if err != nil {
					return fmt.Errorf("%s: %w", t.name, err)
				}
				var got int
				if err := dst.QueryRow("SELECT COUNT(*) FROM " + t.name).Scan(&got); err != nil {
					return err
				}
				if got != n {
					return fmt.Errorf("%s: copied %d rows but the target has %d", t.name, n, got)
				}
				fmt.Printf("%s: %d rows\n", t.name, n)
			}
			if dst.D.PG { // identity columns continue after the copied ids
				for _, t := range []string{"images", "jobs", "job_items"} {
					if _, err := dst.Exec(fmt.Sprintf("SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM %s), 0) + 1, false)", t, t)); err != nil {
						return err
					}
				}
			}
			fmt.Println("done; set PHOTOSORT_DB to the target to use it")
			return nil
		},
	}
	cp.Flags().StringVar(&from, "from", "", "source database URL")
	cp.Flags().StringVar(&to, "to", "", "target database URL")
	cmd.AddCommand(cp)
	return cmd
}

func dedupCols(cols string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range strings.Split(cols, ",") {
		c = strings.TrimSpace(c)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func copyTable(src, dst *db.DB, table string, cols []string) (int, error) {
	rows, err := src.Query("SELECT " + strings.Join(cols, ", ") + " FROM " + table + " ORDER BY 1")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	ins := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "),
		strings.TrimSuffix(strings.Repeat("?,", len(cols)), ","))
	n := 0
	var batch [][]any
	flush := func() error {
		err := dst.Write(func(tx *db.Tx) error {
			for _, v := range batch {
				if _, err := tx.Exec(ins, v...); err != nil {
					return err
				}
			}
			return nil
		})
		batch = batch[:0]
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok { // JSONB arrives as bytes; nothing in the schema is binary
				vals[i] = string(b)
			}
		}
		batch = append(batch, vals)
		n++
		if len(batch) == 500 {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, flush()
}

func modelsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "models", Short: "pose models: list what's installed, fetch more"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "pose models the analyzer can run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			an, stop, err := connectAnalyzer(cmd.Context())
			if err != nil {
				return err
			}
			defer stop()
			h, err := an.Health(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("device: %v\n", h["device"])
			for _, m := range h["models"].([]any) {
				fmt.Printf("  %v\n", m)
			}
			return nil
		},
	})
	return cmd
}
