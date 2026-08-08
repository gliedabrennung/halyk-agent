package facts

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/index"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/store"
)

const _minPageChars = 40

var _txnRefRe = regexp.MustCompile(`\bTXN-([A-Za-z0-9]+)-\d+\b`)

func buildInput(
	ctx context.Context,
	opts Options,
	idx *index.Index,
	scenarioID string,
) (agents.FactsInput, error) {
	const (
		maxDocChars = 14000
		maxOCRPages = 8
	)
	in := agents.FactsInput{ScenarioID: scenarioID}

	var candidates []index.Entry
	for _, e := range idx.Entries {
		if e.ScenarioID != scenarioID || !e.Effective {
			continue
		}
		switch e.DocType {
		case domain.DocAuditReport, domain.DocKYCDossier, domain.DocCorporateStructure, domain.DocFXTable:
			candidates = append(candidates, e)
			continue
		}

		text, err := opts.Store.DocText(e.DocID)
		if err != nil {
			return in, err
		}
		for _, m := range _txnRefRe.FindAllStringSubmatch(text, -1) {
			if m[1] == scenarioID {
				candidates = append(candidates, e)
				break
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].DocID < candidates[j].DocID })

	if docID := idx.GroupParents[scenarioID]; docID != "" {
		text, err := opts.Store.DocText(docID)
		if err != nil {
			return in, err
		}
		in.GroupDoc = &agents.FactsDoc{
			DocID:   docID,
			DocType: string(domain.DocAuditReport),
			Text:    truncate(text, maxDocChars),
		}
	}

	if len(candidates) == 0 {
		return in, fmt.Errorf("%s: no audit report, dossier or memo among its effective documents", scenarioID)
	}

	for _, e := range candidates {
		if in.Company == "" && e.Meta.CompanyName != "" {
			in.Company = e.Meta.CompanyName
		}
		text, err := opts.Store.DocText(e.DocID)
		if err != nil {
			return in, err
		}
		doc := agents.FactsDoc{
			DocID:   e.DocID,
			DocType: string(e.DocType),
			Text:    truncate(text, maxDocChars),
		}

		if e.Scan.Chars < _minPageChars*e.Pages || strings.Contains(text, "\f") {
			pages, err := emptyPages(opts.Store, e.DocID)
			if err != nil {
				return in, err
			}
			var ocr strings.Builder
			for _, p := range pages {
				if ocrPageCount(in)+len(doc.OCRPages) >= maxOCRPages {
					break
				}
				if !ingest.IsPDF(e.Path) {
					continue
				}
				pageText, err := ingest.OCRPage(ctx, e.Path, p, opts.Cfg.CacheDir)
				if err != nil {
					return in, fmt.Errorf("ocr %s p%d: %w", e.DocID, p, err)
				}
				fmt.Fprintf(&ocr, "[page %d]\n%s\n", p, strings.TrimSpace(pageText))
				doc.OCRPages = append(doc.OCRPages, p)
			}
			doc.OCRText = truncate(ocr.String(), maxDocChars)
		}
		in.Documents = append(in.Documents, doc)
	}
	return in, nil
}

func emptyPages(st *store.Store, docID string) ([]int, error) {
	rows, err := st.DB().Query(`SELECT page_no, text FROM pages WHERE doc_id = ? ORDER BY page_no`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		var text string
		if err := rows.Scan(&n, &text); err != nil {
			return nil, err
		}
		if len(strings.Fields(text)) <= 2 && len(strings.TrimSpace(text)) < _minPageChars {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

func ocrPageCount(in agents.FactsInput) int {
	n := 0
	for _, d := range in.Documents {
		n += len(d.OCRPages)
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[... truncated ...]"
}
