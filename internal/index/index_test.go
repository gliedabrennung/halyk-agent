package index

import (
	"strings"
	"testing"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

func txn(id, scn, acc, counterparty string) domain.Txn {
	return domain.Txn{
		ID: id, ScenarioID: scn, AccountID: acc, Counterparty: counterparty,
		Date:   time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
		Amount: decimal.RequireFromString("-100"), Currency: "USD",
	}
}

func testLedger() *domain.Ledger {
	return domain.NewLedger([]domain.Txn{
		txn("TXN-P1-0001", "P1", "ACC-7801", "Caspian Shipping Lines JSC"),
		txn("TXN-P1-0002", "P1", "ACC-7801", "Aktau Holdings L.L.P."),
		txn("TXN-P1-0003", "P1", "ACC-7801", "Shared Utility LLP"),
		txn("TXN-P6-0001", "P6", "ACC-7806", "Ural Grinding Works LLP"),
		txn("TXN-P6-0002", "P6", "ACC-7806", "Taraz Kiln Services LLP"),
		txn("TXN-P6-0003", "P6", "ACC-7806", "Shared Utility LLP"),
	})
}

var _scenarios = []string{"P1", "P6"}

func entry(docID string, meta agents.TriageResult, scan Scan) Entry {
	return Entry{DocID: docID, DocType: meta.DocType, Meta: meta, Scan: scan}
}

func TestResolveByAccountID(t *testing.T) {
	entries := []Entry{
		entry("a", agents.TriageResult{DocType: domain.DocCreditAgreement, CompanyName: "Aktau Port Services JSC"},
			Scan{AccountIDs: []string{"ACC-7801"}, ClauseNumbers: []string{"6.1"}, PeriodFrom: "2025-01-01", PeriodTo: "2025-12-31"}),
	}
	idx := Resolve(entries, testLedger(), _scenarios, "2025")

	got := idx.Entries[0]
	if got.ScenarioID != "P1" || got.ResolvedBy != ResolvedByAccount {
		t.Fatalf("resolved to %q by %q, want P1 by account_id", got.ScenarioID, got.ResolvedBy)
	}
	if !got.Effective {
		t.Error("a current agreement must be effective")
	}
	if idx.Companies["aktau port services"] != "P1" {
		t.Errorf("the company name should have been learned: %v", idx.Companies)
	}
}

func TestResolveRefusesAmbiguousAccounts(t *testing.T) {
	entries := []Entry{
		entry("a", agents.TriageResult{DocType: domain.DocOther},
			Scan{AccountIDs: []string{"ACC-7801", "ACC-7806"}}),
	}
	idx := Resolve(entries, testLedger(), _scenarios, "2025")

	if idx.Entries[0].ScenarioID != "" || idx.Entries[0].ResolvedBy != Unresolved {
		t.Errorf("got %q by %q, want unresolved", idx.Entries[0].ScenarioID, idx.Entries[0].ResolvedBy)
	}
	if len(idx.Entries[0].Notes) == 0 {
		t.Error("the ambiguity must be recorded in the notes")
	}
}

func TestResolveByCompanyNameLearnedFromAccounts(t *testing.T) {
	entries := []Entry{
		entry("with-account", agents.TriageResult{DocType: domain.DocCreditAgreement, CompanyName: "Aktau Port Services JSC"},
			Scan{AccountIDs: []string{"ACC-7801"}}),
		entry("no-account", agents.TriageResult{DocType: domain.DocAuditReport, CompanyName: "Aktau Port Services  JSC"},
			Scan{}),
	}
	idx := Resolve(entries, testLedger(), _scenarios, "2025")

	for _, e := range idx.Entries {
		if e.ScenarioID != "P1" {
			t.Errorf("%s resolved to %q, want P1", e.DocID, e.ScenarioID)
		}
	}
	if idx.Entries[0].ResolvedBy != ResolvedByCompany && idx.Entries[1].ResolvedBy != ResolvedByCompany {
		t.Error("one of the two should have been resolved by company name")
	}
}

func TestResolveDisablesAmbiguousCompanyName(t *testing.T) {
	entries := []Entry{
		entry("a", agents.TriageResult{CompanyName: "Holdings Group"}, Scan{AccountIDs: []string{"ACC-7801"}}),
		entry("b", agents.TriageResult{CompanyName: "Holdings Group"}, Scan{AccountIDs: []string{"ACC-7806"}}),
		entry("c", agents.TriageResult{CompanyName: "Holdings Group"}, Scan{}),
	}
	idx := Resolve(entries, testLedger(), _scenarios, "2025")

	for _, e := range idx.Entries {
		if e.DocID == "c" && e.ScenarioID != "" {
			t.Errorf("c resolved to %q on an ambiguous name", e.ScenarioID)
		}
	}
}

func TestResolveByCounterparties(t *testing.T) {
	entries := []Entry{
		entry("kyc", agents.TriageResult{
			DocType:        domain.DocKYCDossier,
			CompanyName:    "Taraz Cement Works JSC",
			Counterparties: []string{"Ural Grinding Works LLP", "Taraz Kiln Services LLP", "Shared Utility LLP"},
		}, Scan{}),
	}
	idx := Resolve(entries, testLedger(), _scenarios, "2025")

	got := idx.Entries[0]
	if got.ScenarioID != "P6" || got.ResolvedBy != ResolvedByCounterparty {
		t.Fatalf("resolved to %q by %q, want P6 by counterparties", got.ScenarioID, got.ResolvedBy)
	}
}

func TestResolveIgnoresASingleWeakCounterparty(t *testing.T) {
	entries := []Entry{
		entry("x", agents.TriageResult{Counterparties: []string{"Ural Grinding Works LLP"}}, Scan{}),
		entry("y", agents.TriageResult{Counterparties: []string{"Shared Utility LLP"}}, Scan{}),
	}
	idx := Resolve(entries, testLedger(), _scenarios, "2025")

	for _, e := range idx.Entries {
		if e.ScenarioID != "" {
			t.Errorf("%s resolved to %q on one weak counterparty", e.DocID, e.ScenarioID)
		}
	}
}

func TestResolveMarksSupersededRevisions(t *testing.T) {
	tests := []struct {
		name string
		meta agents.TriageResult
		scan Scan
	}{
		{
			name: "banner in the text",
			meta: agents.TriageResult{DocType: domain.DocCreditAgreement},
			scan: Scan{AccountIDs: []string{"ACC-7801"}, Superseded: true, SupersededQuote: "НЕДЕЙСТВУЮЩАЯ РЕДАКЦИЯ"},
		},
		{
			name: "the model read it as a prior revision",
			meta: agents.TriageResult{DocType: domain.DocCreditAgreement, IsSuperseded: true},
			scan: Scan{AccountIDs: []string{"ACC-7801"}},
		},
		{
			name: "covenant period is for another year",
			meta: agents.TriageResult{DocType: domain.DocCreditAgreement},
			scan: Scan{AccountIDs: []string{"ACC-7801"}, PeriodFrom: "2024-01-01", PeriodTo: "2024-12-31"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := Resolve([]Entry{entry("a", tt.meta, tt.scan)}, testLedger(), _scenarios, "2025")
			if idx.Entries[0].Effective {
				t.Error("this revision must not be treated as in force")
			}
			if len(idx.Entries[0].Notes) == 0 {
				t.Error("the reason must be recorded in the notes")
			}
		})
	}
}

func TestCheckCoverage(t *testing.T) {
	clauses := map[string][]string{"P1": {"6.1", "6.2", "6.3"}, "P6": {"6.1", "6.2", "6.3"}}
	full := Scan{AccountIDs: []string{"ACC-7801"}, ClauseNumbers: []string{"6.1", "6.2", "6.3"}}
	fullP6 := Scan{AccountIDs: []string{"ACC-7806"}, ClauseNumbers: []string{"6.1", "6.2", "6.3"}}

	t.Run("complete", func(t *testing.T) {
		idx := Resolve([]Entry{
			entry("a", agents.TriageResult{DocType: domain.DocCreditAgreement}, full),
			entry("b", agents.TriageResult{DocType: domain.DocCreditAgreement}, fullP6),
		}, testLedger(), _scenarios, "2025")
		if err := idx.CheckCoverage(_scenarios, clauses); err != nil {
			t.Errorf("expected complete coverage, got: %v", err)
		}
	})

	t.Run("a borrower has no agreement", func(t *testing.T) {
		idx := Resolve([]Entry{
			entry("a", agents.TriageResult{DocType: domain.DocCreditAgreement}, full),
		}, testLedger(), _scenarios, "2025")
		err := idx.CheckCoverage(_scenarios, clauses)
		if err == nil || !strings.Contains(err.Error(), "P6") {
			t.Errorf("expected P6 to be reported as uncovered, got: %v", err)
		}
	})

	t.Run("only a superseded agreement", func(t *testing.T) {
		stale := full
		stale.Superseded = true
		idx := Resolve([]Entry{
			entry("a", agents.TriageResult{DocType: domain.DocCreditAgreement}, stale),
			entry("b", agents.TriageResult{DocType: domain.DocCreditAgreement}, fullP6),
		}, testLedger(), _scenarios, "2025")
		err := idx.CheckCoverage(_scenarios, clauses)
		if err == nil || !strings.Contains(err.Error(), "P1") {
			t.Errorf("a retired revision must not count as coverage, got: %v", err)
		}
	})

	t.Run("agreement is missing a requested clause", func(t *testing.T) {
		partial := full
		partial.ClauseNumbers = []string{"6.1", "6.2"}
		idx := Resolve([]Entry{
			entry("a", agents.TriageResult{DocType: domain.DocCreditAgreement}, partial),
			entry("b", agents.TriageResult{DocType: domain.DocCreditAgreement}, fullP6),
		}, testLedger(), _scenarios, "2025")
		err := idx.CheckCoverage(_scenarios, clauses)
		if err == nil || !strings.Contains(err.Error(), "6.3") {
			t.Errorf("expected the missing clause to be named, got: %v", err)
		}
	})
}

func TestCreditAgreementsExcludesRetiredAndOtherBorrowers(t *testing.T) {
	idx := Resolve([]Entry{
		entry("current", agents.TriageResult{DocType: domain.DocCreditAgreement},
			Scan{AccountIDs: []string{"ACC-7801"}, PeriodFrom: "2025-01-01", PeriodTo: "2025-12-31"}),
		entry("stale", agents.TriageResult{DocType: domain.DocCreditAgreement},
			Scan{AccountIDs: []string{"ACC-7801"}, PeriodFrom: "2024-01-01", PeriodTo: "2024-12-31"}),
		entry("other-borrower", agents.TriageResult{DocType: domain.DocCreditAgreement},
			Scan{AccountIDs: []string{"ACC-7806"}}),
	}, testLedger(), _scenarios, "2025")

	got := idx.CreditAgreements("P1")
	if len(got) != 1 || got[0].DocID != "current" {
		t.Fatalf("got %v, want just the current P1 agreement", got)
	}
}

func TestPeriodVetoAppliesOnlyToAgreements(t *testing.T) {
	fieldwork := Scan{AccountIDs: []string{"ACC-7801"}, PeriodFrom: "2026-01-15", PeriodTo: "2026-03-20"}

	audit := Resolve([]Entry{
		entry("audit", agents.TriageResult{DocType: domain.DocAuditReport, Period: "FY2025"}, fieldwork),
	}, testLedger(), _scenarios, "2025")
	if !audit.Entries[0].Effective {
		t.Errorf("an audit report was retired by its fieldwork dates: %v", audit.Entries[0].Notes)
	}

	agreement := Resolve([]Entry{
		entry("agmt", agents.TriageResult{DocType: domain.DocCreditAgreement},
			Scan{AccountIDs: []string{"ACC-7801"}, PeriodFrom: "2024-01-01", PeriodTo: "2024-12-31"}),
	}, testLedger(), _scenarios, "2025")
	if agreement.Entries[0].Effective {
		t.Error("an agreement whose covenant period is 2024 must not govern 2025")
	}
}

func TestLinkGroupParentsFindsTheBorrowerInTheBody(t *testing.T) {
	idx := &Index{
		Companies:    map[string]string{"ekibastuz power services": "P5"},
		GroupParents: map[string]string{},
		Entries: []Entry{
			{DocID: "grp", DocType: domain.DocAuditReport},
		},
	}
	body := "Consolidated Financial Statements of Sarybel Energy Holding JSC. " +
		"The Group's thermal generation segment is conducted through Ekibastuz Power Services JSC."

	if err := idx.LinkGroupParents(func(string) (string, error) { return body, nil }); err != nil {
		t.Fatalf("LinkGroupParents: %v", err)
	}
	if got := idx.GroupParents["P5"]; got != "grp" {
		t.Errorf("GroupParents[P5] = %q, want grp", got)
	}
	if idx.Entries[0].ScenarioID != "" {
		t.Error("the report is about the parent; it must not become a document of the borrower")
	}
}

func TestLinkGroupParentsRefusesAmbiguousAndIrrelevantReports(t *testing.T) {
	companies := map[string]string{"alpha works": "P1", "beta works": "P2"}

	two := &Index{
		Companies: companies, GroupParents: map[string]string{},
		Entries: []Entry{{DocID: "grp", DocType: domain.DocAuditReport}},
	}
	if err := two.LinkGroupParents(func(string) (string, error) {
		return "Consolidated statements covering Alpha Works LLP and Beta Works LLP.", nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(two.GroupParents) != 0 {
		t.Errorf("two borrowers in one report must stay unlinked, got %v", two.GroupParents)
	}

	standalone := &Index{
		Companies: companies, GroupParents: map[string]string{},
		Entries: []Entry{{DocID: "solo", DocType: domain.DocAuditReport}},
	}
	if err := standalone.LinkGroupParents(func(string) (string, error) {
		return "Standalone financial statements of Alpha Works LLP.", nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(standalone.GroupParents) != 0 {
		t.Errorf("a report that is not consolidated is not group statements, got %v", standalone.GroupParents)
	}

	resolved := &Index{
		Companies: companies, GroupParents: map[string]string{},
		Entries: []Entry{{DocID: "own", DocType: domain.DocAuditReport, ScenarioID: "P1"}},
	}
	if err := resolved.LinkGroupParents(func(string) (string, error) {
		return "Consolidated statements mentioning Alpha Works LLP.", nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(resolved.GroupParents) != 0 {
		t.Errorf("a borrower's own audit is not the group parent, got %v", resolved.GroupParents)
	}
}
