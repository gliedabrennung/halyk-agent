package ingest

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
)

type Options struct {
	Cfg       *config.Config
	Store     *store.Store
	Log       *slog.Logger
	RenderPNG bool
	Force     bool
}

type Report struct {
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`

	TxnCount       int               `json:"txn_count"`
	ScenarioCount  int               `json:"scenario_count"`
	AccountCount   int               `json:"account_count"`
	Currencies     map[string]int    `json:"currencies"`
	DateFrom       time.Time         `json:"date_from"`
	DateTo         time.Time         `json:"date_to"`
	Counterparties int               `json:"counterparties"`
	PerScenario    []ScenarioSummary `json:"per_scenario"`
	NoiseScenarios int               `json:"noise_scenarios"`
	NoiseTxns      int               `json:"noise_txns"`
	Bijection      []BijectionIssue  `json:"bijection_issues,omitempty"`
	MissingAmounts []string          `json:"missing_amounts,omitempty"`

	TemplateScenarios []string `json:"template_scenarios"`
	CellCount         int      `json:"cell_count"`
	MissingScenarios  []string `json:"missing_scenarios,omitempty"`

	DocCount     int      `json:"doc_count"`
	NonPDFDocs   int      `json:"non_pdf_docs"`
	SkippedFiles []string `json:"skipped_files,omitempty"`
	DocExtracted int      `json:"doc_extracted"`
	DocCached    int      `json:"doc_cached"`
	PageCount    int      `json:"page_count"`
	CharCount    int      `json:"char_count"`
	NeedsOCR     []string `json:"needs_ocr,omitempty"`
	OCRAvailable bool     `json:"ocr_available"`
	PNGRendered  int      `json:"png_rendered"`
}

type ScenarioSummary struct {
	ScenarioID     string          `json:"scenario_id"`
	Accounts       []string        `json:"accounts"`
	Txns           int             `json:"txns"`
	Counterparties int             `json:"counterparties"`
	Outflow        decimal.Decimal `json:"outflow"`
	Inflow         decimal.Decimal `json:"inflow"`
	Currencies     []string        `json:"currencies"`
	InTemplate     bool            `json:"in_template"`
}

const (
	ArtifactKind       = "ingest"
	ArtifactReportID   = "report"
	ArtifactTemplate   = "template"
	ArtifactAccountMap = "account_map"
)

func Run(ctx context.Context, opts Options) (*Report, error) {
	if err := CheckTools(opts.RenderPNG); err != nil {
		return nil, err
	}
	rep := &Report{StartedAt: time.Now(), OCRAvailable: HasTesseract()}

	tpl, err := ParseTemplate(opts.Cfg.TemplatePath())
	if err != nil {
		return nil, err
	}
	rep.TemplateScenarios = tpl.Scenarios
	rep.CellCount = len(tpl.Cells)
	opts.Log.Info("template parsed", "scenarios", len(tpl.Scenarios), "cells", len(tpl.Cells))

	led, err := ParseLedger(opts.Cfg.LedgerPath())
	if err != nil {
		return nil, err
	}
	opts.Log.Info("ledger parsed", "txns", len(led.Txns), "scenarios", len(led.ByScenario))

	rep.Bijection = CheckBijection(led)
	for _, issue := range rep.Bijection {
		opts.Log.Error("account/scenario bijection violated", "issue", issue.String())
	}

	for i := range led.Txns {
		if t := &led.Txns[i]; t.AmountMissing {
			rep.MissingAmounts = append(rep.MissingAmounts, t.ID)
			opts.Log.Warn("ledger row has no amount; the figure must come from a document",
				"txn", t.ID, "scenario", t.ScenarioID, "counterparty", t.Counterparty,
				"description", t.Description, "date", t.Date.Format("2006-01-02"))
		}
	}

	summariseLedger(rep, led, tpl)
	for _, s := range rep.MissingScenarios {
		opts.Log.Error("template scenario has no transactions in the ledger", "scenario", s)
	}

	if err := opts.Store.ReplaceTxns(led.Txns); err != nil {
		return nil, fmt.Errorf("persist ledger: %w", err)
	}
	if err := opts.Store.PutArtifact(ArtifactKind, ArtifactTemplate, tpl); err != nil {
		return nil, err
	}
	if err := opts.Store.PutArtifact(ArtifactKind, ArtifactAccountMap, led.AccountToScn); err != nil {
		return nil, err
	}

	if err := extractDocuments(ctx, opts, rep); err != nil {
		return nil, err
	}

	rep.Duration = time.Since(rep.StartedAt)
	if err := opts.Store.PutArtifact(ArtifactKind, ArtifactReportID, rep); err != nil {
		return nil, err
	}
	return rep, nil
}

func summariseLedger(rep *Report, led *domain.Ledger, tpl *domain.Template) {
	inTemplate := make(map[string]bool, len(tpl.Scenarios))
	for _, s := range tpl.Scenarios {
		inTemplate[s] = true
	}

	rep.TxnCount = len(led.Txns)
	rep.ScenarioCount = len(led.ByScenario)
	rep.AccountCount = len(led.AccountToScn)
	rep.Currencies = make(map[string]int)

	allCP := make(map[string]bool)
	for i := range led.Txns {
		t := &led.Txns[i]
		rep.Currencies[t.Currency]++
		allCP[t.Counterparty] = true
		if rep.DateFrom.IsZero() || t.Date.Before(rep.DateFrom) {
			rep.DateFrom = t.Date
		}
		if t.Date.After(rep.DateTo) {
			rep.DateTo = t.Date
		}
	}
	rep.Counterparties = len(allCP)

	for _, scn := range domain.SortedKeys(led.ByScenario) {
		txns := led.ByScenario[scn]
		sum := ScenarioSummary{
			ScenarioID: scn,
			Accounts:   led.ScnToAccount[scn],
			Txns:       len(txns),
			InTemplate: inTemplate[scn],
			Outflow:    decimal.Zero,
			Inflow:     decimal.Zero,
		}
		cps := make(map[string]bool)
		curr := make(map[string]bool)
		for _, t := range txns {
			cps[t.Counterparty] = true
			curr[t.Currency] = true
			if t.Amount.IsNegative() {
				sum.Outflow = sum.Outflow.Add(t.Amount.Abs())
			} else {
				sum.Inflow = sum.Inflow.Add(t.Amount)
			}
		}
		sum.Counterparties = len(cps)
		sum.Currencies = domain.SortedKeys(curr)

		if sum.InTemplate {
			rep.PerScenario = append(rep.PerScenario, sum)
		} else {
			rep.NoiseScenarios++
			rep.NoiseTxns += sum.Txns
		}
	}

	order := make(map[string]int, len(tpl.Scenarios))
	for i, s := range tpl.Scenarios {
		order[s] = i
	}
	slices.SortStableFunc(rep.PerScenario, func(a, b ScenarioSummary) int {
		return cmp.Compare(order[a.ScenarioID], order[b.ScenarioID])
	})

	for _, s := range tpl.Scenarios {
		if _, ok := led.ByScenario[s]; !ok {
			rep.MissingScenarios = append(rep.MissingScenarios, s)
		}
	}
}

func extractDocuments(ctx context.Context, opts Options, rep *Report) error {
	paths, skipped, err := ListDocumentFiles(opts.Cfg.DocumentsDir())
	if err != nil {
		return err
	}
	rep.DocCount = len(paths)
	rep.SkippedFiles = skipped
	for _, p := range paths {
		if !IsPDF(p) {
			rep.NonPDFDocs++
		}
	}
	for _, s := range skipped {
		opts.Log.Warn("file in documents/ is not a readable document type; ignored", "file", s)
	}
	if len(paths) == 0 {
		return fmt.Errorf("no documents found in %s", opts.Cfg.DocumentsDir())
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(min(runtime.NumCPU(), 8))

	var mu sync.Mutex
	var needsOCR []string
	extracted, cached, pages, chars, pngs := 0, 0, 0, 0, 0

	for _, path := range paths {
		g.Go(func() error {
			docID := DocIDFromPath(path)
			sha, size, err := FileSHA256(path)
			if err != nil {
				return fmt.Errorf("hash %s: %w", docID, err)
			}

			if !opts.Force {
				stored, err := opts.Store.DocumentSHA(docID)
				if err != nil {
					return err
				}
				if stored == sha {
					mu.Lock()
					cached++
					mu.Unlock()
					return nil
				}
			}

			pageTexts, err := ExtractFile(gctx, path)
			if err != nil {
				return err
			}
			doc, docPages := buildDocument(path, sha, size, pageTexts)

			if doc.NeedsOCR && IsPDF(path) {
				if !HasTesseract() {
					opts.Log.Warn("no text layer and tesseract is unavailable; document will be text-empty",
						"doc", docID, "pages", doc.Pages, "chars", doc.Chars)
				} else {
					for i := range docPages {
						text, err := OCRPage(gctx, path, docPages[i].No, opts.Cfg.CacheDir)
						if err != nil {
							return err
						}
						docPages[i].Text = text
					}
					doc, _ = buildDocument(path, sha, size, textsOf(docPages))
					doc.OCRUsed = true
					doc.NeedsOCR = true
				}
			}

			if opts.RenderPNG {
				for p := 1; p <= doc.Pages; p++ {
					if _, err := RenderPagePNG(gctx, path, p, 150, opts.Cfg.CacheDir); err != nil {
						return err
					}
					mu.Lock()
					pngs++
					mu.Unlock()
				}
			}

			if err := opts.Store.UpsertDocument(doc, docPages); err != nil {
				return err
			}

			mu.Lock()
			extracted++
			if doc.NeedsOCR && !doc.OCRUsed {
				needsOCR = append(needsOCR, docID)
			}
			mu.Unlock()
			opts.Log.Debug("extracted", "doc", docID, "pages", doc.Pages, "chars", doc.Chars)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	docs, err := opts.Store.LoadDocuments()
	if err != nil {
		return err
	}
	for _, d := range docs {
		pages += d.Pages
		chars += d.Chars
		if d.NeedsOCR && !d.OCRUsed && !slices.Contains(needsOCR, d.ID) {
			needsOCR = append(needsOCR, d.ID)
		}
	}
	slices.Sort(needsOCR)

	rep.DocExtracted, rep.DocCached = extracted, cached
	rep.PageCount, rep.CharCount = pages, chars
	rep.NeedsOCR, rep.PNGRendered = needsOCR, pngs
	return nil
}

func textsOf(pages []domain.Page) []string {
	out := make([]string, len(pages))
	for i, p := range pages {
		out[i] = p.Text
	}
	return out
}

func (r *Report) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 78)

	fmt.Fprintf(&b, "\n%s\nINGEST REPORT  (%.1fs)\n%s\n", line, r.Duration.Seconds(), line)

	fmt.Fprintf(&b, "\nLEDGER\n")
	fmt.Fprintf(&b, "  transactions      %d\n", r.TxnCount)
	fmt.Fprintf(&b, "  scenarios         %d total = %d in template + %d noise (%d noise txns)\n",
		r.ScenarioCount, len(r.PerScenario), r.NoiseScenarios, r.NoiseTxns)
	fmt.Fprintf(&b, "  accounts          %d\n", r.AccountCount)
	fmt.Fprintf(&b, "  counterparties    %d distinct\n", r.Counterparties)
	fmt.Fprintf(&b, "  dates             %s .. %s\n", r.DateFrom.Format("2006-01-02"), r.DateTo.Format("2006-01-02"))
	fmt.Fprintf(&b, "  currencies        %s\n", domain.JoinPairs(r.Currencies))
	if len(r.MissingAmounts) > 0 {
		fmt.Fprintf(&b, "  BLANK AMOUNTS     %d: %s  (must be recovered from documents)\n",
			len(r.MissingAmounts), strings.Join(r.MissingAmounts, " "))
	}
	if len(r.Bijection) == 0 {
		fmt.Fprintf(&b, "  account↔scenario  bijective ✓\n")
	} else {
		fmt.Fprintf(&b, "  account↔scenario  %d VIOLATIONS:\n", len(r.Bijection))
		for _, i := range r.Bijection {
			fmt.Fprintf(&b, "      %s\n", i.String())
		}
	}

	fmt.Fprintf(&b, "\nTEMPLATE\n")
	fmt.Fprintf(&b, "  scenarios         %d: %s\n", len(r.TemplateScenarios), strings.Join(r.TemplateScenarios, " "))
	fmt.Fprintf(&b, "  cells to answer   %d\n", r.CellCount)
	if len(r.MissingScenarios) > 0 {
		fmt.Fprintf(&b, "  MISSING IN LEDGER %s\n", strings.Join(r.MissingScenarios, " "))
	}

	fmt.Fprintf(&b, "\nBORROWERS\n")
	fmt.Fprintf(&b, "  %-6s %-12s %6s %6s %16s %16s  %s\n", "scn", "account", "txns", "cparty", "outflow", "inflow", "ccy")
	for _, s := range r.PerScenario {
		fmt.Fprintf(&b, "  %-6s %-12s %6d %6d %16s %16s  %s\n",
			s.ScenarioID, strings.Join(s.Accounts, ","), s.Txns, s.Counterparties,
			s.Outflow.StringFixed(2), s.Inflow.StringFixed(2), strings.Join(s.Currencies, "/"))
	}

	fmt.Fprintf(&b, "\nDOCUMENTS\n")
	fmt.Fprintf(&b, "  files             %d (%d pdf + %d plain text); %d extracted this run, %d unchanged\n",
		r.DocCount, r.DocCount-r.NonPDFDocs, r.NonPDFDocs, r.DocExtracted, r.DocCached)
	fmt.Fprintf(&b, "  pages             %d\n", r.PageCount)
	fmt.Fprintf(&b, "  characters        %d\n", r.CharCount)
	if r.PNGRendered > 0 {
		fmt.Fprintf(&b, "  png rendered      %d\n", r.PNGRendered)
	}
	if len(r.SkippedFiles) > 0 {
		fmt.Fprintf(&b, "  ignored           %d: %s\n", len(r.SkippedFiles), strings.Join(r.SkippedFiles, " "))
	}
	if len(r.NeedsOCR) > 0 {
		fmt.Fprintf(&b, "  NO TEXT LAYER     %d docs, OCR available: %v\n", len(r.NeedsOCR), r.OCRAvailable)
		fmt.Fprintf(&b, "                    %s\n", strings.Join(r.NeedsOCR, " "))
		if !r.OCRAvailable {
			fmt.Fprintf(&b, "                    these are invisible to full-text search until OCR runs;\n")
			fmt.Fprintf(&b, "                    install tesseract, or rely on multimodal page reading\n")
		}
	} else {
		fmt.Fprintf(&b, "  text layer        present in every document ✓\n")
	}
	fmt.Fprintf(&b, "%s\n", line)
	return b.String()
}
