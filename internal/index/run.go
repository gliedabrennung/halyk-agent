package index

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"golang.org/x/sync/errgroup"
)

const (
	ArtifactKind = "index"
	ArtifactID   = "doc_index"
)

const CoveredYear = "2025"

type Options struct {
	Cfg    *config.Config
	Store  *store.Store
	Log    *slog.Logger
	Client *llm.Client

	Only []string
}

type Report struct {
	Duration    time.Duration  `json:"duration"`
	Documents   int            `json:"documents"`
	ByType      map[string]int `json:"by_type"`
	ByResolver  map[string]int `json:"by_resolver"`
	Resolved    int            `json:"resolved"`
	Unresolved  []string       `json:"unresolved,omitempty"`
	Superseded  []string       `json:"superseded,omitempty"`
	PerScenario []ScenarioDocs `json:"per_scenario"`
	LowConf     []string       `json:"low_confidence,omitempty"`
	Coverage    string         `json:"coverage_error,omitempty"`
	IndexPath   string         `json:"index_path"`
}

type ScenarioDocs struct {
	ScenarioID string   `json:"scenario_id"`
	Total      int      `json:"total"`
	Agreements []string `json:"agreements"`
	Amendments []string `json:"amendments"`
	Audits     []string `json:"audits"`
	KYC        []string `json:"kyc"`
	Structure  []string `json:"structure"`
	FX         []string `json:"fx"`
	Other      int      `json:"other"`
	Superseded int      `json:"superseded"`
}

func Run(ctx context.Context, opts Options) (*Report, error) {
	const maxTriageChars = 6000
	start := time.Now()

	tpl, txns, err := ingest.LoadTemplateAndTxns(opts.Cfg, opts.Store)
	if err != nil {
		return nil, err
	}
	led := domain.NewLedger(txns)

	docs, err := opts.Store.LoadDocuments()
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no documents in the store; run `halyk-agent ingest` first")
	}
	if len(opts.Only) > 0 {
		var filtered []domain.Document
		for _, d := range docs {
			if slices.Contains(opts.Only, d.ID) {
				filtered = append(filtered, d)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("none of the requested documents exist: %s", strings.Join(opts.Only, ", "))
		}
		docs = filtered
	}

	entries := make([]Entry, len(docs))
	var mu sync.Mutex
	done := 0

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Cfg.MaxConcurrency)

	for i, doc := range docs {
		g.Go(func() error {
			text, err := opts.Store.DocText(doc.ID)
			if err != nil {
				return fmt.Errorf("load text %s: %w", doc.ID, err)
			}
			scan := ScanText(text)

			in := agents.TriageInput{
				DocID:    doc.ID,
				Pages:    doc.Pages,
				Text:     truncate(text, maxTriageChars),
				Findings: describeScan(scan),
			}

			if scan.Chars < 100 && ingest.IsPDF(doc.Path) {
				text, pages, err := ocrPages(gctx, doc, opts.Cfg.CacheDir)
				if err != nil {
					return fmt.Errorf("ocr %s: %w", doc.ID, err)
				}
				in.OCRText = truncate(text, maxTriageChars)
				opts.Log.Info("document has no text layer; reading its pages with OCR",
					"doc", doc.ID, "pages", pages, "chars", len(text))
			}

			res, err := agents.Triage(gctx, opts.Client, in)
			if err != nil {
				return err
			}

			entries[i] = Entry{
				DocID:   doc.ID,
				Path:    doc.Path,
				Pages:   doc.Pages,
				DocType: res.DocType,
				Meta:    *res,
				Scan:    scan,
			}

			mu.Lock()
			done++
			n := done
			mu.Unlock()
			if n%25 == 0 {
				opts.Log.Info("triage progress", "done", n, "total", len(docs))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	clauses := make(map[string][]string, len(tpl.Scenarios))
	for _, scn := range tpl.Scenarios {
		clauses[scn] = tpl.ClausesFor(scn)
	}
	idx := Resolve(entries, led, tpl.Scenarios, CoveredYear)
	if err := idx.LinkGroupParents(opts.Store.DocText); err != nil {
		return nil, fmt.Errorf("link group parents: %w", err)
	}
	for _, scn := range domain.SortedKeys(idx.GroupParents) {
		opts.Log.Info("group statements linked to a borrower", "scenario", scn, "doc", idx.GroupParents[scn])
	}

	if err := opts.Store.PutArtifact(ArtifactKind, ArtifactID, idx); err != nil {
		return nil, err
	}
	indexPath := filepath.Join(opts.Cfg.ArtifactsDir, "doc_index.json")
	if err := store.WriteJSON(indexPath, idx); err != nil {
		return nil, err
	}

	rep := buildReport(idx, tpl.Scenarios)
	rep.Duration = time.Since(start)
	rep.IndexPath = indexPath

	if err := idx.CheckCoverage(tpl.Scenarios, clauses); err != nil {
		rep.Coverage = err.Error()
	}
	return rep, nil
}

func ocrPages(ctx context.Context, doc domain.Document, cacheDir string) (string, int, error) {
	const maxTriageOCRPages = 4
	n := min(doc.Pages, maxTriageOCRPages)
	var b strings.Builder
	for p := 1; p <= n; p++ {
		text, err := ingest.OCRPage(ctx, doc.Path, p, cacheDir)
		if err != nil {
			return "", 0, err
		}
		fmt.Fprintf(&b, "[page %d]\n%s\n", p, strings.TrimSpace(text))
	}
	return b.String(), n, nil
}

func describeScan(s Scan) string {
	var parts []string
	if len(s.AccountIDs) > 0 {
		parts = append(parts, "account ids found: "+strings.Join(s.AccountIDs, ", "))
	}
	if len(s.ClauseNumbers) > 0 {
		list := s.ClauseNumbers
		if len(list) > 20 {
			list = list[:20]
		}
		parts = append(parts, "numbered clauses present: "+strings.Join(list, ", "))
	}
	if s.PeriodFrom != "" {
		parts = append(parts, fmt.Sprintf("stated period: %s .. %s", s.PeriodFrom, s.PeriodTo))
	}
	if len(s.Currencies) > 0 {
		parts = append(parts, "currencies: "+strings.Join(s.Currencies, ", "))
	}
	if s.Superseded {
		parts = append(parts, "contains a superseded-revision marker: "+s.SupersededQuote)
	}
	if len(parts) == 0 {
		return ""
	}
	return "- " + strings.Join(parts, "\n- ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	head := n * 3 / 4
	tail := n - head
	return s[:head] + "\n[... truncated ...]\n" + s[len(s)-tail:]
}

func buildReport(idx *Index, scenarios []string) *Report {
	rep := &Report{
		Documents:  len(idx.Entries),
		ByType:     make(map[string]int),
		ByResolver: make(map[string]int),
	}
	for _, e := range idx.Entries {
		rep.ByType[string(e.DocType)]++
		rep.ByResolver[e.ResolvedBy]++
		if e.ScenarioID != "" {
			rep.Resolved++
		} else {
			rep.Unresolved = append(rep.Unresolved, e.DocID)
		}
		if !e.Effective {
			rep.Superseded = append(rep.Superseded, e.DocID)
		}
		if e.Meta.Confidence < 0.6 {
			rep.LowConf = append(rep.LowConf, fmt.Sprintf("%s(%.2f,%s)", e.DocID, e.Meta.Confidence, e.DocType))
		}
	}
	slices.Sort(rep.Unresolved)
	slices.Sort(rep.Superseded)

	for _, scn := range scenarios {
		sd := ScenarioDocs{ScenarioID: scn}
		for _, e := range idx.Entries {
			if e.ScenarioID != scn {
				continue
			}
			sd.Total++
			if !e.Effective {
				sd.Superseded++
				continue
			}
			switch e.DocType {
			case domain.DocCreditAgreement:
				sd.Agreements = append(sd.Agreements, e.DocID)
			case domain.DocAmendment:
				sd.Amendments = append(sd.Amendments, e.DocID)
			case domain.DocAuditReport:
				sd.Audits = append(sd.Audits, e.DocID)
			case domain.DocKYCDossier:
				sd.KYC = append(sd.KYC, e.DocID)
			case domain.DocCorporateStructure:
				sd.Structure = append(sd.Structure, e.DocID)
			case domain.DocFXTable:
				sd.FX = append(sd.FX, e.DocID)
			default:
				sd.Other++
			}
		}
		rep.PerScenario = append(rep.PerScenario, sd)
	}
	return rep
}

func (r *Report) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 78)
	fmt.Fprintf(&b, "\n%s\nTRIAGE REPORT  (%.1fs)\n%s\n", line, r.Duration.Seconds(), line)

	fmt.Fprintf(&b, "\nDOCUMENTS  %d total, %d resolved to a borrower\n", r.Documents, r.Resolved)
	fmt.Fprintf(&b, "  by type       ")
	for _, k := range domain.SortedKeys(r.ByType) {
		fmt.Fprintf(&b, "%s=%d ", k, r.ByType[k])
	}
	fmt.Fprintf(&b, "\n  resolved by   ")
	for _, k := range domain.SortedKeys(r.ByResolver) {
		fmt.Fprintf(&b, "%s=%d ", k, r.ByResolver[k])
	}
	fmt.Fprintf(&b, "\n  superseded    %d\n", len(r.Superseded))
	if len(r.LowConf) > 0 {
		fmt.Fprintf(&b, "  low confidence %d: %s\n", len(r.LowConf), strings.Join(r.LowConf, " "))
	}

	fmt.Fprintf(&b, "\nPER BORROWER (effective documents only)\n")
	fmt.Fprintf(&b, "  %-5s %5s %5s %5s %5s %5s %5s %5s %5s\n",
		"scn", "docs", "agmt", "amnd", "audit", "kyc", "struct", "fx", "super")
	for _, s := range r.PerScenario {
		fmt.Fprintf(&b, "  %-5s %5d %5d %5d %5d %5d %5d %5d %5d\n",
			s.ScenarioID, s.Total, len(s.Agreements), len(s.Amendments), len(s.Audits),
			len(s.KYC), len(s.Structure), len(s.FX), s.Superseded)
	}

	fmt.Fprintf(&b, "\nwrote %s\n", r.IndexPath)
	if r.Coverage != "" {
		fmt.Fprintf(&b, "\nCOVERAGE PROBLEM\n%s\n", r.Coverage)
	} else {
		fmt.Fprintf(&b, "coverage: every borrower has an effective credit agreement with the requested clauses ✓\n")
	}
	fmt.Fprintf(&b, "%s\n", line)
	return b.String()
}
