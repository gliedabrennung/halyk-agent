package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/classify"
	"github.com/gliedabrennung/halyk-agent/internal/covenants"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/evaluate"
	"github.com/gliedabrennung/halyk-agent/internal/facts"
	"github.com/gliedabrennung/halyk-agent/internal/index"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/report"
	"github.com/gliedabrennung/halyk-agent/internal/stability"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/gliedabrennung/halyk-agent/internal/submit"
	"github.com/spf13/cobra"
)

func validateSubmission(app *App) error {
	tpl, ledger, err := loadTemplateAndLedger(app)
	if err != nil {
		return err
	}
	rep, err := submit.Validate(app.Cfg.SubmissionPath(), tpl, ledger)
	if err != nil {
		return err
	}
	fmt.Print(rep.String())
	if !rep.OK() {
		return fmt.Errorf("submission is not scoreable: %d problems", len(rep.Problems))
	}
	return nil
}

func (app *App) requireLLM() error {
	if err := app.Cfg.CheckDataset(); err != nil {
		return err
	}
	return app.Cfg.RequireAPIKey()
}

func (app *App) llmClient(stage string) (*llm.Client, error) {
	if err := app.requireLLM(); err != nil {
		return nil, err
	}
	return llm.New(app.Cfg, app.Store, app.stageLog(stage)), nil
}

func loadTemplateAndLedger(app *App) (*domain.Template, submit.LedgerIndex, error) {
	if err := app.Cfg.CheckDataset(); err != nil {
		return nil, nil, err
	}
	tpl, txns, err := ingest.LoadTemplateAndTxns(app.Cfg, app.Store)
	if err != nil {
		return nil, nil, err
	}
	return tpl, submit.LedgerIndexFromTxns(txns), nil
}

func newIngestCmd(app *App) *cobra.Command {
	var renderPNG bool
	var force bool
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Phase 0 — parse the ledger, the template and every PDF (no LLM)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Cfg.CheckDataset(); err != nil {
				return err
			}
			rep, err := ingest.Run(cmd.Context(), ingest.Options{
				Cfg:       app.Cfg,
				Store:     app.Store,
				Log:       app.stageLog("ingest"),
				RenderPNG: renderPNG,
				Force:     force,
			})
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			return nil
		},
	}
	cmd.Flags().BoolVar(&renderPNG, "render-png", false, "pre-render every page to PNG so later OCR reads from cache")
	cmd.Flags().BoolVar(&force, "force", false, "re-extract documents even if the cached SHA-256 matches")
	return cmd
}

func newTriageCmd(app *App) *cobra.Command {
	var only []string
	var allowIncomplete bool
	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Phase 1 — classify each document and resolve it to a borrower (LLM)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := app.llmClient("triage")
			if err != nil {
				return err
			}
			rep, err := index.Run(cmd.Context(), index.Options{
				Cfg:    app.Cfg,
				Store:  app.Store,
				Log:    app.stageLog("triage"),
				Client: client,
				Only:   only,
			})
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			if rep.Coverage != "" && !allowIncomplete {
				return fmt.Errorf("%s", rep.Coverage)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "triage just these document ids (debugging)")
	cmd.Flags().BoolVar(&allowIncomplete, "allow-incomplete", false,
		"report missing credit agreements instead of failing (use with --only)")
	return cmd
}

func newCovenantsCmd(app *App) *cobra.Command {
	var only []string
	var criticPasses int
	cmd := &cobra.Command{
		Use:   "covenants",
		Short: "Phase 2 — extract an executable CovenantSpec per clause, with a critic loop (LLM)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := app.llmClient("covenants")
			if err != nil {
				return err
			}
			rep, err := covenants.Run(cmd.Context(), covenants.Options{
				Cfg:          app.Cfg,
				Store:        app.Store,
				Log:          app.stageLog("covenants"),
				Client:       client,
				Only:         only,
				CriticPasses: criticPasses,
			})
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			if !rep.OK() {
				return fmt.Errorf("%d of %d covenant cells have no specification",
					len(rep.Failed), rep.Expected)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "extract just these scenarios (e.g. P1,B4)")
	cmd.Flags().IntVar(&criticPasses, "critic-passes", 0, "critic iterations per clause (default 2)")
	return cmd
}

// newReviewCmd — команда-читалка: печатает разбор стора по каждому названному сценарию.
func newReviewCmd(app *App, use, short string, review func(*store.Store, string) (string, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			for _, scn := range args {
				out, err := review(app.Store, scn)
				if err != nil {
					return err
				}
				fmt.Print(out)
			}
			return nil
		},
	}
}

func newFactsCmd(app *App) *cobra.Command {
	var only []string
	cmd := &cobra.Command{
		Use:   "facts",
		Short: "Phase 3 — auditor adjustments, ownership graph and FX rates (LLM + rules)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := app.llmClient("facts")
			if err != nil {
				return err
			}
			rep, err := facts.Run(cmd.Context(), facts.Options{
				Cfg: app.Cfg, Store: app.Store, Log: app.stageLog("facts"),
				Client: client,
				Only:   only,
			})
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			if !rep.OK() {
				return fmt.Errorf("%d borrowers have no fact base", len(rep.Failed))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "extract just these scenarios")
	return cmd
}

func newClassifyCmd(app *App) *cobra.Command {
	var only []string
	cmd := &cobra.Command{
		Use:   "classify",
		Short: "Phase 4 — give every ledger row a category and a related-party flag (rules + LLM)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := app.llmClient("classify")
			if err != nil {
				return err
			}
			rep, err := classify.Run(cmd.Context(), classify.Options{
				Cfg: app.Cfg, Store: app.Store, Log: app.stageLog("classify"),
				Client: client,
				Only:   only,
			})
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			if !rep.OK() {
				return fmt.Errorf("%d transactions have no category", rep.Unknown)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "classify just these scenarios")
	return cmd
}

func newEvaluateCmd(app *App) *cobra.Command {
	var only []string
	critic := true
	cmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Phases 5-6 — deterministic engine, leave-one-out evidence, per-cell critic",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Cfg.CheckDataset(); err != nil {
				return err
			}
			opts := evaluate.Options{
				Cfg:   app.Cfg,
				Store: app.Store,
				Log:   app.stageLog("evaluate"),
				Only:  only,
			}
			if critic {
				if err := app.Cfg.RequireAPIKey(); err != nil {
					return fmt.Errorf("the per-cell critic needs a key (%w); pass --critic=false to skip it", err)
				}
				opts.Client = llm.New(app.Cfg, app.Store, app.stageLog("evaluate"))
			}
			rep, err := evaluate.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			if !rep.OK() {
				return fmt.Errorf("%d cells were not answered by the engine", len(rep.Missing))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "evaluate just these scenarios")
	cmd.Flags().BoolVar(&critic, "critic", true, "have a model read each finished cell against its clause")
	return cmd
}

func newSubmitCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "submit",
		Short: "Phase 7 — fill the template in place and write out/submission.json",
		RunE: func(*cobra.Command, []string) error {
			if err := app.Cfg.CheckDataset(); err != nil {
				return err
			}
			res, err := submit.Run(submit.Options{
				Cfg:   app.Cfg,
				Store: app.Store,
				Log:   app.stageLog("submit"),
			})
			if err != nil {
				return err
			}
			fmt.Printf("wrote %s\n", res.Path)
			fmt.Printf("      %d cells: %d from the engine, %d from the baseline placeholder\n",
				res.Cells, res.FromEngine, res.FromBaseline)
			fmt.Printf("wrote %s\n", res.AuditPath)
			return nil
		},
	}
}

func newReportCmd(app *App) *cobra.Command {
	var only []string
	var file string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render the final PDF report: every borrower, every covenant, the reasoning behind each cell",
		Long: "Reads the submission that will be scored and the reasoning stored behind it —\n" +
			"covenant specifications, fact bases, row labels and engine traces — and renders\n" +
			"out/report.pdf. Needs no model: everything it prints is already in the store.",
		RunE: func(*cobra.Command, []string) error {
			if err := app.Cfg.CheckDataset(); err != nil {
				return err
			}
			rep, err := report.Run(report.Options{
				Cfg: app.Cfg, Store: app.Store, Log: app.stageLog("report"),
				Only: only, Path: file,
			})
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "report just these scenarios")
	cmd.Flags().StringVar(&file, "file", "", "where to write the PDF (default <out>/report.pdf)")
	return cmd
}

// errNoUsableOutput помечает стадию, после которой продолжать модельные стадии бессмысленно:
// они ничего не смогут прочитать и только сожгут квоту.
var errNoUsableOutput = errors.New("nothing the next stages can use")

type runStage struct {
	name string
	llm  bool
	fn   func(log *slog.Logger) error
}

func newRunCmd(app *App) *cobra.Command {
	var skipLLM bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run every stage in order",
		Long: "Runs the pipeline end to end and always finishes by writing out/submission.json:\n" +
			"a stage that fails degrades the cells it feeds instead of aborting the run, because an\n" +
			"unanswered cell scores exactly like a wrong one. Cells the engine could not compute keep\n" +
			"the baseline placeholder. The exit code is non-zero when anything degraded, and the\n" +
			"closing summary names every stage that did. --deterministic runs only the stages that\n" +
			"need no model, which leaves every cell on the placeholder.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Cfg.CheckDataset(); err != nil {
				return err
			}
			client := llm.New(app.Cfg, app.Store, app.Log)

			// ingest — единственная критичная стадия: без леджера и шаблона submission не собрать.
			ingestLog := app.stageLog("ingest")
			ingestLog.Info("stage start")
			ingested, err := ingest.Run(cmd.Context(), ingest.Options{
				Cfg: app.Cfg, Store: app.Store, Log: ingestLog,
			})
			if err != nil {
				return fmt.Errorf("stage ingest: %w", err)
			}
			fmt.Print(ingested.String())

			stages := []runStage{
				{name: "triage", llm: true, fn: func(log *slog.Logger) error {
					if err := app.Cfg.RequireAPIKey(); err != nil {
						return fmt.Errorf("%w: %w", errNoUsableOutput, err)
					}
					rep, err := index.Run(cmd.Context(), index.Options{
						Cfg: app.Cfg, Store: app.Store, Log: log,
						Client: client.WithLogger(log),
					})
					if err != nil {
						return err
					}
					fmt.Print(rep.String())
					if rep.Resolved == 0 {
						return fmt.Errorf("%w: no document was resolved to a borrower", errNoUsableOutput)
					}
					if rep.Coverage != "" {
						return fmt.Errorf("%s", rep.Coverage)
					}
					return nil
				}},
				{name: "covenants", llm: true, fn: func(log *slog.Logger) error {
					rep, err := covenants.Run(cmd.Context(), covenants.Options{
						Cfg: app.Cfg, Store: app.Store, Log: log,
						Client: client.WithLogger(log),
					})
					if err != nil {
						return err
					}
					fmt.Print(rep.String())
					if rep.Specs == 0 {
						return fmt.Errorf("%w: not a single covenant specification was extracted", errNoUsableOutput)
					}
					if !rep.OK() {
						return fmt.Errorf("%d of %d covenant cells have no specification",
							len(rep.Failed), rep.Expected)
					}
					return nil
				}},
				{name: "facts", llm: true, fn: func(log *slog.Logger) error {
					rep, err := facts.Run(cmd.Context(), facts.Options{
						Cfg: app.Cfg, Store: app.Store, Log: log,
						Client: client.WithLogger(log),
					})
					if err != nil {
						return err
					}
					fmt.Print(rep.String())
					if rep.Scenarios == 0 {
						return fmt.Errorf("%w: no borrower got a fact base", errNoUsableOutput)
					}
					if !rep.OK() {
						return fmt.Errorf("%d borrowers have no fact base", len(rep.Failed))
					}
					return nil
				}},
				{name: "classify", llm: true, fn: func(log *slog.Logger) error {
					rep, err := classify.Run(cmd.Context(), classify.Options{
						Cfg: app.Cfg, Store: app.Store, Log: log,
						Client: client.WithLogger(log),
					})
					if err != nil {
						return err
					}
					fmt.Print(rep.String())
					if len(rep.Rows) == 0 {
						return fmt.Errorf("%w: no borrower got a labelled ledger", errNoUsableOutput)
					}
					if !rep.OK() {
						return fmt.Errorf("%d transactions have no category", rep.Unknown)
					}
					return nil
				}},
				{name: "evaluate", fn: func(log *slog.Logger) error {
					opts := evaluate.Options{Cfg: app.Cfg, Store: app.Store, Log: log}
					if !skipLLM {
						opts.Client = client.WithLogger(log)
					}
					rep, err := evaluate.Run(cmd.Context(), opts)
					if err != nil {
						return err
					}
					fmt.Print(rep.String())
					if !rep.OK() {
						return fmt.Errorf("%d cells were not answered by the engine", len(rep.Missing))
					}
					return nil
				}},
			}

			var degraded []string
			for _, st := range stages {
				log := app.stageLog(st.name)
				if skipLLM && st.llm {
					log.Info("skipping stage", "reason", "deterministic run")
					continue
				}
				log.Info("stage start")
				err := st.fn(log)
				if err == nil {
					continue
				}
				log.Error("stage degraded; the run still finishes with a submission", "err", err)
				degraded = append(degraded, fmt.Sprintf("%s: %v", st.name, err))
				if errors.Is(err, errNoUsableOutput) {
					log.Error("skipping the remaining model stages", "reason", "they would read nothing")
					break
				}
			}

			// Отсюда и до конца — безусловно. По условию задачи пустая ячейка стоит ровно
			// столько же, сколько неверная, поэтому файл должен появиться при любом исходе.
			submitLog := app.stageLog("submit")
			submitLog.Info("stage start")
			res, err := submit.Run(submit.Options{Cfg: app.Cfg, Store: app.Store, Log: submitLog})
			if err != nil {
				return fmt.Errorf("stage submit: %w", err)
			}
			fmt.Printf("wrote %s (%d cells: %d engine, %d baseline)\n",
				res.Path, res.Cells, res.FromEngine, res.FromBaseline)

			reportLog := app.stageLog("report")
			reportLog.Info("stage start")
			if rep, err := report.Run(report.Options{Cfg: app.Cfg, Store: app.Store, Log: reportLog}); err != nil {
				reportLog.Error("stage degraded; the submission is unaffected", "err", err)
				degraded = append(degraded, fmt.Sprintf("report: %v", err))
			} else {
				fmt.Print(rep.String())
			}

			if err := validateSubmission(app); err != nil {
				return err
			}
			if len(degraded) > 0 {
				return fmt.Errorf("%s is written and scoreable, but %d stage(s) degraded:\n  - %s",
					res.Path, len(degraded), strings.Join(degraded, "\n  - "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&skipLLM, "deterministic", false, "run only the stages that need no model (produces a baseline submission)")
	return cmd
}

func newStabilityCmd(app *App) *cobra.Command {
	var passes int
	var only []string
	cmd := &cobra.Command{
		Use:   "stability",
		Short: "Re-run the model stages with a fresh cache and report which cells move",
		Long: "Salts the response cache, re-extracts the covenant specs, the fact base and the\n" +
			"row labels into a namespace of their own, evaluates them and compares the cells\n" +
			"with the stored answer. The submission is never touched.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.requireLLM(); err != nil {
				return err
			}
			rep, err := stability.Run(cmd.Context(), stability.Options{
				Cfg: app.Cfg, Store: app.Store, Log: app.stageLog("stability"), Passes: passes, Only: only,
			})
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			return nil
		},
	}
	cmd.Flags().IntVar(&passes, "passes", 1, "independent re-runs to compare against the stored answer")
	cmd.Flags().StringSliceVar(&only, "only", nil, "probe just these scenarios")
	return cmd
}

func newValidateCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check out/submission.json against the template: keys, types, filled cells, evidence ids",
		RunE: func(*cobra.Command, []string) error {
			return validateSubmission(app)
		},
	}
}

func newScoreCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "score",
		Short: "Score out/submission.json against data/ground_truth.json (local dev only)",
		Long: "Applies the challenge's own scoring rule to the public ground truth that ships\n" +
			"with the dataset. Development aid only — the pipeline itself never reads it.",
		RunE: func(*cobra.Command, []string) error {
			tpl, _, err := loadTemplateAndLedger(app)
			if err != nil {
				return err
			}
			rep, err := submit.Score(app.Cfg.SubmissionPath(), app.Cfg.GroundTruthPath(), tpl)
			if err != nil {
				return err
			}
			fmt.Print(rep.String())
			return nil
		},
	}
}

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what is already in the store: transactions, documents, cached LLM calls",
		RunE: func(*cobra.Command, []string) error {
			txns, err := app.Store.LoadTxns()
			if err != nil {
				return err
			}
			docs, err := app.Store.LoadDocuments()
			if err != nil {
				return err
			}
			pages := 0
			for _, d := range docs {
				pages += d.Pages
			}
			n, tin, tout, err := app.Store.CacheStats()
			if err != nil {
				return err
			}
			fmt.Printf("data dir:      %s\n", app.Cfg.DataDir)
			fmt.Printf("database:      %s\n", app.Cfg.DBPath)
			fmt.Printf("transactions:  %d\n", len(txns))
			fmt.Printf("documents:     %d (%d pages)\n", len(docs), pages)
			fmt.Printf("llm cache:     %d responses, %d in / %d out tokens\n", n, tin, tout)
			return nil
		},
	}
}

func newLLMCheckCmd(app *App) *cobra.Command {
	var modelName string
	cmd := &cobra.Command{
		Use:   "llm-check",
		Short: "Send one trivial prompt to verify the key, the model name and the provider wiring",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Cfg.RequireAPIKey(); err != nil {
				return err
			}
			if modelName == "" {
				modelName = app.Cfg.Model
			}
			client := llm.New(app.Cfg, app.Store, app.Log)

			const prompt = `Reply with exactly this JSON and nothing else: {"ok": true, "lang": "ru"}`
			start := time.Now()
			out, err := client.Complete(cmd.Context(), llm.Request{
				Name:          "llm_check",
				Model:         modelName,
				Description:   "Connectivity check.",
				Instruction:   "You are a health check. Answer with strict JSON only.",
				Prompt:        prompt,
				SchemaVersion: "llm-check-v1",
				JSON:          true,
			})
			if err != nil {
				return err
			}
			fmt.Printf("model:    %s\n", modelName)
			fmt.Printf("elapsed:  %s\n", time.Since(start).Round(time.Millisecond))
			fmt.Printf("response: %s\n", strings.TrimSpace(out))

			n, tin, tout, err := app.Store.CacheStats()
			if err != nil {
				return err
			}
			fmt.Printf("cache:    %d responses, %d in / %d out tokens\n", n, tin, tout)
			return nil
		},
	}
	cmd.Flags().StringVar(&modelName, "model", "", "model to test (default: the configured fast model)")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		RunE: func(*cobra.Command, []string) error {
			info, ok := debug.ReadBuildInfo()
			if !ok {
				return fmt.Errorf("build info unavailable")
			}
			rev, dirty := "unknown", ""
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					rev = s.Value
				case "vcs.modified":
					if s.Value == "true" {
						dirty = " (dirty)"
					}
				}
			}
			fmt.Printf("halyk-agent %s%s built with %s\n", rev, dirty, info.GoVersion)
			return nil
		},
	}
}
