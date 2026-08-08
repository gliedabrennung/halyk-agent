package facts

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/index"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"golang.org/x/sync/errgroup"
)

const ArtifactKind = "facts"

type Options struct {
	Cfg    *config.Config
	Store  *store.Store
	Log    *slog.Logger
	Client *llm.Client
	Only   []string

	Namespace string
}

func Run(ctx context.Context, opts Options) (*Report, error) {
	start := time.Now()

	tpl, txns, err := ingest.LoadTemplateAndTxns(opts.Cfg, opts.Store)
	if err != nil {
		return nil, err
	}
	idx, err := index.Load(opts.Store)
	if err != nil {
		return nil, err
	}

	scenarios, err := tpl.ScenariosFor(opts.Only)
	if err != nil {
		return nil, err
	}

	results := make([]*domain.FactBase, len(scenarios))
	inputs := make([]agents.FactsInput, len(scenarios))
	failures := make([]string, len(scenarios))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Cfg.MaxConcurrency)
	var mu sync.Mutex

	for i, scn := range scenarios {
		g.Go(func() error {
			in, err := buildInput(gctx, opts, idx, scn)
			if err != nil {
				return err
			}
			in.MissingAmounts = missingAmountTxns(txns, scn)
			mu.Lock()
			inputs[i] = in
			mu.Unlock()

			fb, err := agents.ExtractFacts(gctx, opts.Client, opts.Cfg.Model, in)
			if err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				opts.Log.Error("fact extraction failed", "scenario", scn, "err", err)
				mu.Lock()
				failures[i] = fmt.Sprintf("%s: %v", scn, err)
				mu.Unlock()
				return nil
			}
			results[i] = fb
			opts.Log.Info("facts extracted", "scenario", scn,
				"docs", len(in.Documents), "ocr_pages", ocrPageCount(in),
				"adjustments", len(fb.Adjustments), "parties", len(fb.Parties),
				"threshold", fb.RelatedPartyThreshold.String())
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	dir := filepath.Join(opts.Cfg.ArtifactsDir, "facts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	rep := &Report{Path: dir}
	for i, fb := range results {
		if f := failures[i]; f != "" {
			rep.Failed = append(rep.Failed, f)
			continue
		}
		if fb == nil {
			continue
		}
		if err := opts.Store.PutArtifact(ArtifactKind+opts.Namespace, fb.ScenarioID, fb); err != nil {
			return nil, err
		}

		if opts.Namespace == "" {
			if err := store.WriteJSON(filepath.Join(dir, fb.ScenarioID+".json"), fb); err != nil {
				return nil, err
			}
		}
		for _, id := range unfixedAmounts(inputs[i].MissingAmounts, fb) {
			opts.Log.Warn("the ledger left this row without an amount and no document states it",
				"scenario", fb.ScenarioID, "txn", id)
			rep.Unfixed = append(rep.Unfixed, fmt.Sprintf("%s/%s", fb.ScenarioID, id))
		}
		rep.Rows = append(rep.Rows, summarise(fb, len(inputs[i].Documents), ocrPageCount(inputs[i])))
		rep.TotalOCRPage += ocrPageCount(inputs[i])
		rep.Scenarios++
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

func unfixedAmounts(requested []string, fb *domain.FactBase) []string {
	fixed := make(map[string]bool, len(fb.Adjustments))
	for _, a := range fb.Adjustments {
		if a.Kind == domain.AdjLedgerAmountFix && a.Applied && a.Amount.IsPositive() {
			fixed[a.TxnID] = true
		}
	}
	var out []string
	for _, id := range requested {
		if !fixed[id] {
			out = append(out, id)
		}
	}
	return out
}

func missingAmountTxns(txns []domain.Txn, scenarioID string) []string {
	var out []string
	for _, t := range txns {
		if t.ScenarioID == scenarioID && t.AmountMissing {
			out = append(out, t.ID)
		}
	}
	slices.Sort(out)
	return out
}
