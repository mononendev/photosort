package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mononendev/photosort/internal/backends"
	"github.com/mononendev/photosort/internal/db"
	"github.com/mononendev/photosort/internal/pj"
)

// submitWhere are the images a batch submit picks up: still to tag, and not in a batch that hasn't come back yet.
func submitWhere(d *db.DB) string {
	return "local_json IS NOT NULL AND " + d.D.VLMTodo() +
		" AND (batch_id IS NULL OR batch_id IN (SELECT id FROM batches WHERE fetched=1))"
}

// withCrop counts the analyzed rows that have a subject crop (a person) to send along with the frame.
func withCrop(rows []db.Image) int {
	n := 0
	for _, r := range rows {
		if pj.F(pj.Parse(r.LocalJSON)["n_people"]) > 0 {
			n++
		}
	}
	return n
}

func estimateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "estimate",
		Short: "cost table for pending images across models",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			cfg := a.cfg.Get()
			n, _ := a.db.Count(submitWhere(a.db))
			if n == 0 {
				n, _ = a.db.Count("")
			}
			rows, err := a.db.Rows("local_json IS NOT NULL", nil, "", -1, 0, "id, local_json")
			if err != nil {
				return err
			}
			wc := int(float64(n) * 0.9)
			without := n - wc
			if len(rows) > 0 {
				wc = withCrop(rows)
				without = len(rows) - wc
			}
			fmt.Printf("%d images pending (%d with a subject crop). Batch price is what you pay; interactive shown for reference.\n", n, wc)
			fmt.Printf("%-26s %12s %11s %12s %9s\n", "model", "in tokens", "out tokens", "interactive", "batch")
			fmt.Printf("%-26s %12s %11s %12s %9s   (GPU time instead; see docs/DEVLOG.md)\n", "ollama (local, qwen3-vl)", "-", "-", "free", "free")
			for _, p := range backends.Prices {
				name := "anthropic"
				if strings.HasPrefix(p.Model, "gemini") {
					name = "gemini"
				}
				b, err := backends.Get(name, "")
				if err != nil {
					return err
				}
				e := b.Estimate(p.Model, wc, without, cfg)
				fmt.Printf("%-26s %12s %11s %11.2f$ %8.2f$\n", p.Model, commas(int64(e.InputTokens)), commas(int64(e.OutputTokens)),
					e.InteractiveUSD, e.BatchUSD)
			}
			return nil
		},
	}
}

func itemsFor(rows []db.Image, cache string) ([]backends.Item, error) {
	out := make([]backends.Item, len(rows))
	for i, r := range rows {
		it, err := backends.LoadItem(cache, r.ID, *r.LocalJSON)
		if err != nil {
			return nil, err
		}
		out[i] = it
	}
	return out, nil
}

func submitCmd() *cobra.Command {
	var backend, model, baseURL string
	var concurrency, sample, batchSize int
	var seed int64
	var skipTier0, retry, dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "stage 2: send images to the vision model (batch jobs for cloud backends)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			cfg := a.cfg.Get()
			bname := backend
			if bname == "" {
				bname = pj.Str(cfg["backend"])
			}
			be, err := backends.Get(bname, pj.Str(pj.Or(baseURL, cfg["base_url"])))
			if err != nil {
				return err
			}
			mname := pj.Str(pj.Or(model, pj.Or(cfg["model"], be.DefaultModel())))
			where := submitWhere(a.db)
			if !retry {
				where += " AND error IS NULL"
			}
			rows, err := a.db.Rows(where, nil, "", -1, 0, "")
			if err != nil {
				return err
			}
			if skipTier0 {
				kept := rows[:0]
				for _, r := range rows {
					if pj.F(pj.Parse(r.LocalJSON)["local_tier"]) > 0 {
						kept = append(kept, r)
					}
				}
				rows = kept
			}
			if sample > 0 {
				rand.New(rand.NewPCG(uint64(seed), 0)).Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
				rows = rows[:min(sample, len(rows))]
			}
			if len(rows) == 0 {
				fmt.Println("nothing to submit (run `local` first, or everything is already submitted)")
				return nil
			}
			wc := withCrop(rows)
			est := be.Estimate(mname, wc, len(rows)-wc, cfg)
			note := ""
			if !est.Priced {
				note = " (model not in price table)"
			}
			fmt.Printf("backend=%s model=%s images=%d est. input tokens=%s batch cost ≈ $%v%s\n", bname, mname, len(rows),
				commas(int64(est.InputTokens)), est.BatchUSD, note)
			cache := a.cacheDir()
			if dryRun {
				if g, ok := be.(*backends.Gemini); ok {
					items, err := itemsFor(rows[:min(3, len(rows))], cache)
					if err != nil {
						return err
					}
					p := filepath.Join(a.workdir, "dry-run.jsonl")
					if err := g.WriteJSONL(items, mname, cfg, p); err != nil {
						return err
					}
					st, _ := os.Stat(p)
					fmt.Printf("dry run: wrote %d requests to %s (%d KB); nothing uploaded\n", len(items), p, st.Size()/1024)
				} else {
					items, err := itemsFor(rows[:1], cache)
					if err != nil {
						return err
					}
					req, err := be.BuildRequest(items[0], mname, cfg)
					if err != nil {
						return err
					}
					b, _ := json.MarshalIndent(backends.Redact(req), "", " ")
					fmt.Println(string(b[:min(3000, len(b))]))
				}
				return nil
			}
			if be.Sync() {
				return runSync(cmd.Context(), a, be, mname, rows, cfg, concurrency)
			}
			if !yes {
				fmt.Print("submit? [y/N] ")
				ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(strings.ToLower(ans)) != "y" {
					return nil
				}
			}
			size := batchSize
			if size == 0 {
				size = pj.Int(cfg["batch_size"])
			}
			size = min(size, be.MaxBatch())
			for i := 0; i < len(rows); i += size {
				chunk := rows[i:min(i+size, len(rows))]
				items, err := itemsFor(chunk, cache)
				if err != nil {
					return err
				}
				bid, err := be.Submit(cmd.Context(), items, mname, cfg, a.workdir)
				if err != nil {
					return err
				}
				if err := a.db.AddBatch(bid, bname, mname, len(chunk), db.Now()); err != nil {
					return err
				}
				ids := make([]int64, len(chunk))
				for k, r := range chunk {
					ids[k] = r.ID
				}
				if err := a.db.SetBatch(ids, bid); err != nil {
					return err
				}
				fmt.Printf("submitted batch %s (%d images)\n", bid, len(chunk))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&backend, "backend", "", "gemini | anthropic | ollama (default: config backend)")
	f.StringVar(&model, "model", "", "model name (default: config model, else the backend's)")
	f.StringVar(&baseURL, "base-url", "", "ollama server, e.g. http://k8s-5:11434 (or OLLAMA_HOST)")
	f.IntVar(&concurrency, "concurrency", 2, "parallel requests for sync backends (match OLLAMA_NUM_PARALLEL)")
	f.BoolVar(&skipTier0, "skip-local-tier0", false, "don't send images the local stage scored as nothing-in-focus")
	f.BoolVar(&retry, "retry-errors", false, "also send images that failed before")
	f.IntVar(&sample, "sample", 0, "submit only N random images (calibration run)")
	f.Int64Var(&seed, "seed", 0, "shuffle seed for --sample")
	f.IntVar(&batchSize, "batch-size", 0, "requests per batch (default: config batch_size)")
	f.BoolVar(&dryRun, "dry-run", false, "build the requests, send nothing")
	f.BoolVarP(&yes, "yes", "y", false, "don't ask before submitting")
	return cmd
}

// runSync classifies each image now through a synchronous backend (a local model server), storing as it goes.
func runSync(ctx context.Context, a *app, be backends.Backend, model string, rows []db.Image, cfg pj.Obj, conc int) error {
	fmt.Printf("running %d images through %s at %s with %s, concurrency %d\n", len(rows), be.Name(), be.BaseURL(), model, conc)
	t0 := time.Now()
	var mu sync.Mutex
	ok, bad := 0, 0
	var secs []float64
	work := make(chan db.Image)
	var wg sync.WaitGroup
	for range max(1, conc) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range work {
				if ctx.Err() != nil {
					continue
				}
				item, err := backends.LoadItem(a.cacheDir(), r.ID, *r.LocalJSON)
				var res backends.Result
				if err != nil {
					res = backends.Result{Key: strconv.FormatInt(r.ID, 10), Error: err.Error()}
				} else {
					res = be.Classify(ctx, item, model, cfg)
				}
				stored := storeResult(a.db, res)
				mu.Lock()
				if stored {
					ok++
				} else {
					bad++
				}
				if s, has := pj.Float(res.Usage["seconds"]); has && s != 0 {
					secs = append(secs, s)
				}
				n := ok + bad
				mu.Unlock()
				fmt.Fprintf(os.Stderr, "\r%d/%d", n, len(rows))
			}
		}()
	}
	for _, r := range rows {
		work <- r
	}
	close(work)
	wg.Wait()
	fmt.Fprintln(os.Stderr)
	dt := time.Since(t0).Seconds()
	avg := 0.0
	for _, s := range secs {
		avg += s
	}
	if len(secs) > 0 {
		avg /= float64(len(secs))
	}
	fmt.Printf("done: %d ok, %d errors, %.0fs total, %.1fs/img wall, %.1fs/img server latency\n", ok, bad, dt,
		dt/float64(max(1, len(rows))), avg)
	return a.printUsage()
}

// storeResult stores a backend result for the image it is keyed by; says whether it carried data.
func storeResult(d *db.DB, res backends.Result) bool {
	id, err := strconv.ParseInt(res.Key, 10, 64)
	if err != nil {
		return false
	}
	ok := pj.Truthy(res.Data)
	var data, errMsg *string
	if ok {
		s := pj.Dumps(res.Data)
		data = &s
	} else {
		e := res.Error
		errMsg = &e
	}
	if err := d.SetVLM(id, data, pj.DumpsPtr(res.Usage), errMsg); err != nil {
		return false
	}
	return ok
}

func pollCmd() *cobra.Command {
	var wait bool
	var interval int
	cmd := &cobra.Command{
		Use:   "poll",
		Short: "check batches and fetch finished results",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.close()
			ctx := cmd.Context()
			for {
				pending, err := a.db.Batches(true)
				if err != nil {
					return err
				}
				if len(pending) == 0 {
					fmt.Println("no pending batches")
					break
				}
				remaining := 0
				for _, b := range pending {
					be, err := backends.Get(b.Backend, "")
					if err != nil {
						return err
					}
					st, err := be.Status(ctx, b.ID)
					if err != nil {
						return err
					}
					switch {
					case st == "ended":
						results, err := be.Fetch(ctx, b.ID)
						if err != nil {
							return err
						}
						ok := 0
						for _, res := range results {
							if _, err := strconv.ParseUint(res.Key, 10, 64); err != nil {
								continue
							}
							if storeResult(a.db, res) {
								ok++
							}
						}
						a.db.SetBatchState(b.ID, "ended", true)
						fmt.Printf("batch %s: %d/%d parsed ok\n", b.ID, ok, len(results))
						// anything submitted but missing from results can be resubmitted
						a.db.ClearBatch(b.ID, false)
					case strings.HasPrefix(st, "failed"):
						a.db.SetBatchState(b.ID, st, true)
						a.db.ClearBatch(b.ID, true)
						fmt.Printf("batch %s: %s; images released for resubmission\n", b.ID, st)
					default:
						remaining++
						a.db.SetBatchState(b.ID, st, false)
					}
				}
				if remaining == 0 || !wait {
					if remaining > 0 {
						fmt.Printf("%d batch(es) still running; re-run `poll` or use --wait\n", remaining)
					}
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(interval) * time.Second):
				}
			}
			return a.printUsage()
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "keep polling until every batch has ended")
	cmd.Flags().IntVar(&interval, "interval", 120, "seconds between polls with --wait")
	return cmd
}
