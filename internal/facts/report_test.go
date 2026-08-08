package facts

import (
	"strings"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// The applied/rejected split is the one number a reader checks first: a
// disclosure the auditor considered and refused must never be counted as one
// they accepted.
func TestSummariseSeparatesAppliedFromRejected(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P4",
		Adjustments: []domain.Adjustment{
			{Kind: domain.AdjEBITDAAddBack, Amount: dec("100"), Applied: true},
			{Kind: domain.AdjEBITDAAddBack, Amount: dec("200"), Applied: true},
			{Kind: domain.AdjNoChange, Applied: false},
		},
	}

	row := summarise(fb, 2, 1)

	if row.ScenarioID != "P4" || row.Docs != 2 || row.OCRPages != 1 {
		t.Errorf("row header = %+v", row)
	}
	if row.Applied != 2 || row.Rejected != 1 {
		t.Errorf("applied/rejected = %d/%d, want 2/1", row.Applied, row.Rejected)
	}
	if row.Kinds != "ebitda_add_back=2 no_change=1" {
		t.Errorf("kinds = %q", row.Kinds)
	}
}

// Relatedness is decided by the threshold, and a borrower whose file states
// none must show that rather than an invented zero.
func TestSummariseCountsRelatedPartiesAgainstTheThreshold(t *testing.T) {
	parties := []domain.Party{
		{Name: "Alpha LLP", VotingShare: dec("33.4"), Related: true},
		{Name: "Beta LLP", VotingShare: dec("12.0")},
		{Name: "Gamma LLP", VotingShare: dec("40.1"), Related: true},
	}

	withThreshold := summarise(&domain.FactBase{
		Parties:               parties,
		RelatedPartyThreshold: dec("30"),
	}, 1, 0)
	if withThreshold.Parties != 3 || withThreshold.Related != 2 {
		t.Errorf("parties/related = %d/%d, want 3/2", withThreshold.Parties, withThreshold.Related)
	}
	if withThreshold.Threshold != "30%" {
		t.Errorf("threshold = %q, want 30%%", withThreshold.Threshold)
	}

	none := summarise(&domain.FactBase{Parties: parties}, 1, 0)
	if none.Threshold != "—" {
		t.Errorf("threshold = %q, want an em dash when the file states none", none.Threshold)
	}
}

func TestSummariseListsDisclosedRates(t *testing.T) {
	withRate := summarise(&domain.FactBase{
		FXRates: map[string]decimal.Decimal{"EUR": dec("1.16")},
	}, 1, 0)
	if withRate.FX != "EUR=1.16" {
		t.Errorf("fx = %q, want EUR=1.16", withRate.FX)
	}

	var none domain.FactBase
	if got := summarise(&none, 1, 0).FX; got != "—" {
		t.Errorf("fx = %q, want an em dash when no rate is disclosed", got)
	}
}

func TestReportOKOnlyWhenNoBorrowerFailed(t *testing.T) {
	var clean Report
	if !clean.OK() {
		t.Error("a report with no failures is OK")
	}
	failed := Report{Failed: []string{"P3: unusable after repair"}}
	if failed.OK() {
		t.Error("a failed borrower must make the stage not OK")
	}
}

// The stage summary is what a human reads before trusting the run, so it has to
// carry the failure, not bury it.
func TestReportStringShowsFailures(t *testing.T) {
	rep := &Report{
		Scenarios: 1,
		Rows:      []Row{{ScenarioID: "P1", Docs: 2, Threshold: "20%", FX: "—"}},
		Failed:    []string{"P3: no fact base"},
		Path:      "/tmp/facts",
	}

	out := rep.String()
	for _, want := range []string{"P1", "FAILED", "P3: no fact base", "/tmp/facts"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary does not mention %q:\n%s", want, out)
		}
	}
}
