package facts

import (
	"fmt"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

type Report struct {
	Duration     time.Duration `json:"duration"`
	Scenarios    int           `json:"scenarios"`
	Rows         []Row         `json:"rows"`
	Failed       []string      `json:"failed,omitempty"`
	Path         string        `json:"path"`
	TotalOCRPage int           `json:"total_ocr_pages"`
}

type Row struct {
	ScenarioID string `json:"scenario_id"`
	Docs       int    `json:"docs"`
	OCRPages   int    `json:"ocr_pages"`
	Applied    int    `json:"applied"`
	Rejected   int    `json:"rejected"`
	Parties    int    `json:"parties"`
	Related    int    `json:"related"`
	Threshold  string `json:"threshold"`
	FX         string `json:"fx"`
	Kinds      string `json:"kinds"`
}

func (r *Report) OK() bool { return len(r.Failed) == 0 }

func summarise(fb *domain.FactBase, docs, ocrPages int) Row {
	row := Row{
		ScenarioID: fb.ScenarioID,
		Docs:       docs,
		OCRPages:   ocrPages,
		Parties:    len(fb.Parties),
		Related:    len(fb.RelatedNames()),
	}
	if fb.RelatedPartyThreshold.IsPositive() {
		row.Threshold = fb.RelatedPartyThreshold.String() + "%"
	} else {
		row.Threshold = "—"
	}
	kinds := make(map[string]int, len(fb.Adjustments))
	for _, a := range fb.Adjustments {
		if a.Applied {
			row.Applied++
		} else {
			row.Rejected++
		}
		kinds[a.Kind]++
	}
	row.Kinds = domain.JoinPairs(kinds)
	row.FX = domain.JoinPairs(fb.FXRates)
	if row.FX == "" {
		row.FX = "—"
	}
	return row
}

func (r *Report) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 104)
	fmt.Fprintf(&b, "\n%s\nFACT BASE  (%.1fs)  %d borrowers, %d pages read by OCR\n%s\n",
		line, r.Duration.Seconds(), r.Scenarios, r.TotalOCRPage, line)
	fmt.Fprintf(&b, "  %-5s %5s %6s %8s %8s %7s %7s %10s %-8s %s\n",
		"scn", "docs", "ocr", "applied", "rejected", "parties", "related", "threshold", "fx", "kinds")
	for _, row := range r.Rows {
		fmt.Fprintf(&b, "  %-5s %5d %6d %8d %8d %7d %7d %10s %-8s %s\n",
			row.ScenarioID, row.Docs, row.OCRPages, row.Applied, row.Rejected,
			row.Parties, row.Related, row.Threshold, row.FX, row.Kinds)
	}
	fmt.Fprintf(&b, "%s\n", line)
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, "  FAILED:\n")
		for _, f := range r.Failed {
			fmt.Fprintf(&b, "    %s\n", f)
		}
	}
	fmt.Fprintf(&b, "wrote %s/<scenario>.json\n%s\n", r.Path, line)
	return b.String()
}
