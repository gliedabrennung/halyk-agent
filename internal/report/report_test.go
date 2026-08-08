package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/engine"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestMoney(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"0", "0.00"},
		{"1.5", "1.50"},
		{"999.994", "999.99"},
		{"1000", "1 000.00"},
		{"1234567.891", "1 234 567.89"},
		{"-42327826.28", "-42 327 826.28"},
	} {
		if got := money(dec(tt.in)); got != tt.want {
			t.Errorf("money(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCleanKeepsCyrillicAndDropsAstral(t *testing.T) {
	got := clean("Заёмщик pays \U0001F600 now")
	if !strings.Contains(got, "Заёмщик") {
		t.Fatalf("clean dropped Cyrillic: %q", got)
	}
	for _, r := range got {
		if r > 0xFFFF {
			t.Fatalf("clean kept an astral rune: %q", got)
		}
	}
	if strings.ContainsRune(got, '') {
		t.Errorf("clean kept a control character: %q", got)
	}
}

func TestCollapse(t *testing.T) {
	got := collapse("line one\n  line   two\ttab\n")
	if want := "line one line two tab"; got != want {
		t.Errorf("collapse = %q, want %q", got, want)
	}
}

func TestCellHelpers(t *testing.T) {
	c := cell{
		ScenarioID: "P1", ClauseID: "6.1",
		Verdict: &domain.Verdict{
			Source:     engine.SourceEngine,
			Confidence: 0.35,
			Trace: []string{
				"capex = 100.00 over 1 capex row(s)",
				"metric capex = 100.0000 (threshold <= 42)",
				"critic disagrees (wrong_line): opex excludes the rent line",
			},
		},
	}
	if !c.Reasoned() {
		t.Error("cell backed by an engine verdict should count as reasoned")
	}
	if !c.Disputed() {
		t.Error("a trace with a critic disagreement should be disputed")
	}
	if !c.Flagged() {
		t.Errorf("confidence %.2f should be flagged", c.Confidence())
	}
	if got := len(c.Trace()); got != 2 {
		t.Errorf("Trace() returned %d lines, want the 2 engine lines only", got)
	}
	if !strings.HasPrefix(c.Critic(), "critic disagrees") {
		t.Errorf("Critic() = %q", c.Critic())
	}
	if got, want := noteOf(&c), "critic disagrees, low confidence"; got != want {
		t.Errorf("noteOf = %q, want %q", got, want)
	}

	baseline := cell{Verdict: &domain.Verdict{Source: engine.SourceBaseline, Confidence: 1}}
	if baseline.Reasoned() {
		t.Error("a baseline verdict is not reasoned")
	}
	if got := noteOf(&baseline); !strings.Contains(got, "baseline") {
		t.Errorf("noteOf = %q, want it to mention the baseline", got)
	}
}

func TestSourceLabel(t *testing.T) {
	c := cell{ScenarioID: "P1", Spec: &domain.CovenantSpec{
		SourceRef: domain.PageRef{DocID: "P1", Page: 0, Quote: "…"},
	}}
	if got, want := sourceLabel(&c), "clause text"; got != want {
		t.Errorf("sourceLabel = %q, want %q (an unknown page must not print as zero)", got, want)
	}
	c.Spec.SourceRef = domain.PageRef{DocID: "a5cc1400b640", Page: 7}
	if got, want := sourceLabel(&c), "clause text — a5cc1400b640 p. 7"; got != want {
		t.Errorf("sourceLabel = %q, want %q", got, want)
	}
	c.Spec.SourceRef = domain.PageRef{DocID: "a5cc1400b640"}
	if got, want := sourceLabel(&c), "clause text — a5cc1400b640"; got != want {
		t.Errorf("sourceLabel = %q, want %q", got, want)
	}
}

func TestPeriodOf(t *testing.T) {
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	got := periodOf(domain.Period{Kind: "fiscal_year", From: from, To: to, Label: "2025"})
	if want := "fiscal_year  2025-01-01 .. 2025-12-31  (2025)"; got != want {
		t.Errorf("periodOf = %q, want %q", got, want)
	}
	if got := periodOf(domain.Period{Kind: "trailing_12m"}); got != "trailing_12m" {
		t.Errorf("periodOf without dates = %q", got)
	}
}

func TestRender(t *testing.T) {
	d := sampleDossier()
	r := newRenderer("Covenant compliance report")
	render(r, d)

	if err := r.pdf.Error(); err != nil {
		t.Fatalf("render: %v", err)
	}
	if pages := r.pdf.PageNo(); pages < 4 {
		t.Errorf("rendered %d pages, want at least a cover, a summary, a borrower and an appendix", pages)
	}

	var buf bytes.Buffer
	if err := r.pdf.Output(&buf); err != nil {
		t.Fatalf("output: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Error("output is not a PDF")
	}
	if !bytes.Contains(buf.Bytes(), []byte("%%EOF")) {
		t.Error("output is not terminated")
	}
	if buf.Len() < 2000 {
		t.Errorf("output is %d bytes, too small to hold the report", buf.Len())
	}
}

func TestRenderWithoutReasoning(t *testing.T) {
	d := &dossier{
		Generated:      time.Now().UTC(),
		SubmissionPath: "out/submission.json",
		Corpus:         corpus{Borrowers: 1, Cells: 1, Compliant: 1},
		Borrowers: []borrower{{
			ScenarioID: "P1",
			Cells: []cell{{
				ScenarioID: "P1", ClauseID: "6.1",
				Status: domain.StatusCompliant, Actual: dec("1"),
			}},
		}},
		Warnings: []string{"P1: no covenant specifications (run `halyk-agent covenants`)"},
	}
	r := newRenderer("Covenant compliance report")
	render(r, d)
	if err := r.pdf.Error(); err != nil {
		t.Fatalf("render: %v", err)
	}
}

func sampleDossier() *dossier {
	evidence := domain.Txn{
		ID: "TXN-P1-0010", ScenarioID: "P1",
		Date:         time.Date(2025, 5, 21, 0, 0, 0, 0, time.UTC),
		Counterparty: "Ural Crane Works LLP",
		Description:  "Purchase of quayside crane equipment",
		Amount:       dec("-1842006.44"), Currency: "USD",
	}
	spec := &domain.CovenantSpec{
		ScenarioID: "P1", ClauseID: "6.1", Title: "Maximum Capital Intensity Ratio",
		Expression: "capex / (opex + rent)", Op: "<=", Threshold: dec("0.42"), Unit: "ratio",
		Period: domain.Period{
			Kind: "fiscal_year",
			From: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
		},
		Terms: []domain.Term{
			{Name: "capex", Kind: domain.TermStatementLine, Line: "совокупные капитальные затраты"},
			{Name: "floor", Kind: domain.TermConstant, Constant: dec("1000")},
		},
		Carveouts: []domain.Carveout{{
			Description: "flood remediation is excluded",
			Condition:   domain.Condition{Expression: "one_off_items > 0"},
		}},
		Trigger:      &domain.Condition{Expression: "revenue > 0", Description: "only if the year had revenue"},
		EvidenceKind: "single_txn",
		SourceRef: domain.PageRef{
			DocID: "P1",
			Quote: "Заёмщик, Aktau Port Services JSC, обязуется не допускать, чтобы коэффициент " +
				"капиталоёмкости за период с 2025-01-01 по 2025-12-31 превышал 0.42x.",
		},
	}
	verdict := &domain.Verdict{
		ScenarioID: "P1", ClauseID: "6.1", Status: domain.StatusBreach, Actual: dec("0.4578"),
		Source: engine.SourceEngine, Confidence: 1, CarveoutApplied: "flood remediation",
		Trace: []string{
			"capex = 1842006.44 over 1 capex row(s)",
			"metric capex / (opex + rent) = 0.4578 (threshold <= 0.42)",
			"critic: the figures follow from the clause",
		},
	}

	return &dossier{
		Generated: time.Date(2026, 8, 7, 10, 8, 25, 0, time.UTC), SubmissionPath: "out/submission.json",
		Corpus: corpus{
			Borrowers: 1, Cells: 2, Compliant: 1, Breach: 1, WithEvidence: 1,
			Reasoned: 1, Flagged: 1, Disputed: 0,
			Txns: 1473, Documents: 202, Pages: 845,
		},
		Borrowers: []borrower{{
			ScenarioID: "P1", Company: "Aktau Port Services JSC",
			Accounts: []string{"ACC-7801"}, Txns: 56,
			Related: []string{"Aktau Holdings LLP"}, RelatedThreshold: dec("20"),
			FX:        map[string]decimal.Decimal{"EUR": dec("1.08")},
			FactNotes: []string{"Обеспечительное покрытие дочерних организаций отсутствует."},
			Adjustments: []domain.Adjustment{{
				Kind: domain.AdjExcludePeriod, TxnID: "TXN-P1-0045", Amount: dec("0"),
				Applied: true, Rationale: "Обследование причальной стенки проводилось вне периода",
				FromCategory: "capex", ToCategory: "operating_costs",
			}},
			Docs: []docRef{
				{DocID: "a5cc1400b640", DocType: domain.DocCreditAgreement, Pages: 12},
				{DocID: "6f1c06f8479a", DocType: domain.DocAuditReport, Pages: 9},
			},
			Totals: []categoryTotal{
				{Category: domain.CatTaxes, Amount: dec("-42327826.28"), Txns: 9},
				{Category: domain.CatRevenue, Amount: dec("6842117.53"), Txns: 1},
			},
			Cells: []cell{
				{
					ScenarioID: "P1", ClauseID: "6.1", Title: spec.Title,
					Status: domain.StatusBreach, Actual: dec("0.46"),
					Evidence: evidence.ID, EvidenceTxn: &evidence,
					Spec: spec, Verdict: verdict,
				},
				{
					ScenarioID: "P1", ClauseID: "6.2",
					Status: domain.StatusCompliant, Actual: dec("6842117.53"),
				},
			},
		}},
		Warnings: []string{"P1/6.2: answered by the baseline placeholder, not by the engine"},
	}
}
