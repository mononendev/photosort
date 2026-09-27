package main

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/mononendev/photosort/internal/export"
	"github.com/mononendev/photosort/internal/pj"
)

func sortCmd() *cobra.Command {
	var link, source, xmp string
	var overwrite bool
	cmd := &cobra.Command{
		Use:   "sort OUT",
		Short: "stage 3: link tree, CSV/JSONL and XMP sidecars",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !slices.Contains([]string{"symlink", "hardlink", "copy", "move"}, link) {
				return fmt.Errorf("--link must be symlink, hardlink, copy or move")
			}
			if !slices.Contains([]string{"sidecar", "outdir", "none"}, xmp) {
				return fmt.Errorf("--xmp must be sidecar, outdir or none")
			}
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			cfg := a.cfg.Get()
			if source == "" {
				source = pj.Str(cfg["focus_source"])
			}
			rows, err := a.db.Rows("local_json IS NOT NULL", nil, "", -1, 0, "")
			if err != nil {
				return err
			}
			recs := make([]*export.Record, len(rows))
			for i, r := range rows {
				if recs[i], err = export.FinalRecord(exportRow(r), source); err != nil {
					return err
				}
			}
			out, _ := filepath.Abs(args[0])
			if err := export.Export(recs, out); err != nil {
				return err
			}
			groups := pj.O(cfg, "groups")
			counts, err := export.BuildTree(recs, out, link, groups)
			if err != nil {
				return err
			}
			sorted := 0
			for _, c := range counts {
				if c.Key != "review" {
					sorted += c.N
				}
			}
			fmt.Printf("sorted %d images into %s using %s: %s\n", sorted, out, link, counts.String())
			if xmp != "none" {
				xd := ""
				if xmp == "outdir" {
					xd = filepath.Join(out, "xmp")
				}
				w, s, err := export.WriteXMP(recs, xd, overwrite, groups)
				if err != nil {
					return err
				}
				hint := ""
				if w == 0 && s > 0 {
					hint = " (use --xmp-overwrite)"
				}
				fmt.Printf("XMP: wrote %d, skipped %d existing%s\n", w, s, hint)
			}
			fmt.Printf("exports: %s, %s\n", filepath.Join(out, "results.csv"), filepath.Join(out, "results.jsonl"))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&link, "link", "symlink", "symlink | hardlink | copy | move")
	f.StringVar(&source, "focus-source", "", "vlm | local | strict (default: config focus_source)")
	f.StringVar(&xmp, "xmp", "sidecar", "sidecar (next to the originals) | outdir | none")
	f.BoolVar(&overwrite, "xmp-overwrite", false, "replace existing XMP files")
	return cmd
}
