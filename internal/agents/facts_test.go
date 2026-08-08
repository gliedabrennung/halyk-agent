package agents

import (
	"strings"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

var _factsInput = FactsInput{ScenarioID: "P2", Company: "Almaty Cold Chain JSC"}

const _factsJSONBody = `{
  "adjustments": [
    {"kind": "reclassify", "counterparty": "Tien Shan Advisory Bureau", "amount": "1104663.28",
     "from_category": "Консультационные услуги", "to_category": "Операционные расходы",
     "rationale": "текущее управление складскими операциями", "applied": true,
     "source_doc": "3fd0d34546b5", "quote": "Сумма в размере $1,104,663.28 ..."}
  ],
  "parties": [
    {"name": "Almaty Chill Logistics LLP",   "voting_share": "8.6"},
    {"name": "Tien Shan Advisory Bureau",    "voting_share": "23.4"},
    {"name": "Zhetysu Capital Partners LLP", "voting_share": "31.2"}
  ],
  "related_party_threshold": "25.0",
  "fx_rates": [],
  "notes": [],
  "confidence": 0.9
}`

func TestParseFactsAppliesTheBorrowersOwnThreshold(t *testing.T) {
	fb, err := parseFacts(_factsJSONBody, _factsInput)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if !fb.RelatedPartyThreshold.Equal(mustDec("25")) {
		t.Errorf("threshold = %s, want 25", fb.RelatedPartyThreshold)
	}
	want := map[string]bool{
		"Almaty Chill Logistics LLP":   false,
		"Tien Shan Advisory Bureau":    false,
		"Zhetysu Capital Partners LLP": true,
	}
	for _, p := range fb.Parties {
		if p.Related != want[p.Name] {
			t.Errorf("%s (%s%%): related = %v, want %v", p.Name, p.VotingShare, p.Related, want[p.Name])
		}
	}
	if names := fb.RelatedNames(); len(names) != 1 || names[0] != "Zhetysu Capital Partners LLP" {
		t.Errorf("RelatedNames = %v", names)
	}
}

func TestRelatednessIsInclusiveOfTheThreshold(t *testing.T) {
	body := strings.Replace(_factsJSONBody, `"voting_share": "23.4"`, `"voting_share": "25.0"`, 1)
	fb, err := parseFacts(body, _factsInput)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	for _, p := range fb.Parties {
		if p.Name == "Tien Shan Advisory Bureau" && !p.Related {
			t.Error("a share exactly at the threshold must count as related")
		}
	}
}

func TestThresholdIsPerBorrower(t *testing.T) {
	body := strings.Replace(_factsJSONBody, `"related_party_threshold": "25.0"`, `"related_party_threshold": "40.0"`, 1)
	fb, err := parseFacts(body, _factsInput)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if len(fb.RelatedNames()) != 0 {
		t.Errorf("at a 40%% threshold nobody here qualifies, got %v", fb.RelatedNames())
	}
}

func TestRejectedAdjustmentsAreNeverApplied(t *testing.T) {
	body := strings.Replace(_factsJSONBody,
		`{"kind": "reclassify", "counterparty": "Tien Shan Advisory Bureau", "amount": "1104663.28",`,
		`{"kind": "no_change", "counterparty": "Tengiz Risk Engineering", "amount": "118447.52",`, 1)
	body = strings.Replace(body, `"applied": true`, `"applied": true`, 1)

	fb, err := parseFacts(body, _factsInput)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if len(fb.Adjustments) != 1 {
		t.Fatalf("adjustments = %d", len(fb.Adjustments))
	}
	if fb.Adjustments[0].Applied {
		t.Error("a no_change adjustment must be recorded as not applied, even when the model says otherwise")
	}
	if len(fb.AppliedAdjustments()) != 0 {
		t.Error("AppliedAdjustments must exclude it")
	}
}

func TestParseFactsRejectsUnknownAdjustmentKind(t *testing.T) {
	body := strings.Replace(_factsJSONBody, `"kind": "reclassify"`, `"kind": "vibes"`, 1)
	if _, err := parseFacts(body, _factsInput); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("expected an unknown kind to be rejected, got %v", err)
	}
}

func TestParseFactsRejectsEmptyAdjustment(t *testing.T) {
	body := strings.Replace(_factsJSONBody,
		`"amount": "1104663.28"`, `"amount": ""`, 1)
	if _, err := parseFacts(body, _factsInput); err == nil {
		t.Error("an adjustment with no amount and no txn id must be rejected")
	}
}

func TestParseFactsReadsFXRate(t *testing.T) {
	body := strings.Replace(_factsJSONBody, `"fx_rates": []`,
		`"fx_rates": [{"currency": "EUR", "usd_rate": "1.16", "basis": "invoice of 72146.75 EUR settled for USD 83690.23", "quote": "..."}]`, 1)
	fb, err := parseFacts(body, _factsInput)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if got := fb.FXRates["EUR"]; !got.Equal(mustDec("1.16")) {
		t.Errorf("EUR rate = %s, want 1.16", got)
	}
	if len(fb.Notes) == 0 {
		t.Error("the basis for a rate must survive into the notes")
	}
}

func TestParseFactsRejectsNonPositiveFX(t *testing.T) {
	body := strings.Replace(_factsJSONBody, `"fx_rates": []`,
		`"fx_rates": [{"currency": "EUR", "usd_rate": "0"}]`, 1)
	if _, err := parseFacts(body, _factsInput); err == nil {
		t.Error("a zero exchange rate must be rejected")
	}
}

func TestVotingSharesTolerateAPercentSign(t *testing.T) {
	body := strings.Replace(_factsJSONBody, `"voting_share": "31.2"`, `"voting_share": "31.2%"`, 1)
	fb, err := parseFacts(body, _factsInput)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	for _, p := range fb.Parties {
		if p.Name == "Zhetysu Capital Partners LLP" && !p.VotingShare.Equal(mustDec("31.2")) {
			t.Errorf("share = %s, want 31.2", p.VotingShare)
		}
	}
}

func TestAdjustmentKindVocabulary(t *testing.T) {
	for _, k := range []string{
		domain.AdjReclassify, domain.AdjExcludePeriod, domain.AdjIncludePeriod,
		domain.AdjDisclosedAmount, domain.AdjLedgerAmountFix, domain.AdjEBITDAAddBack, domain.AdjNoChange,
	} {
		if !validAdjustmentKind(k) {
			t.Errorf("%q should be a valid adjustment kind", k)
		}
	}
	if validAdjustmentKind("") {
		t.Error("an empty kind must not be valid")
	}
}

func TestDisclosureNamingATransactionBecomesALedgerFix(t *testing.T) {
	body := strings.Replace(_factsJSONBody,
		`{"kind": "reclassify", "counterparty": "Tien Shan Advisory Bureau", "amount": "1104663.28",`,
		`{"kind": "disclosed_amount", "txn_id": "TXN-P7-0033", "counterparty": "State Revenue Committee", "amount": "486204.19",`, 1)
	fb, err := parseFacts(body, _factsInput)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	got := fb.Adjustments[0]
	if got.Kind != domain.AdjLedgerAmountFix {
		t.Errorf("kind = %q, want %q", got.Kind, domain.AdjLedgerAmountFix)
	}
	if got.TxnID != "TXN-P7-0033" || !got.Amount.Equal(mustDec("486204.19")) {
		t.Errorf("adjustment = %+v", got)
	}
}

func TestDisclosureWithoutATransactionStaysADisclosure(t *testing.T) {
	body := strings.Replace(_factsJSONBody,
		`{"kind": "reclassify", "counterparty": "Tien Shan Advisory Bureau", "amount": "1104663.28",`,
		`{"kind": "disclosed_amount", "amount": "918447.52",`, 1)
	fb, err := parseFacts(body, _factsInput)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if fb.Adjustments[0].Kind != domain.AdjDisclosedAmount {
		t.Errorf("kind = %q, want it unchanged", fb.Adjustments[0].Kind)
	}
}

func TestBlankFXRateIsSkippedNotFatal(t *testing.T) {
	raw := `{"adjustments":[{"kind":"reclassify","counterparty":"Acme LLP","amount":"100.00",
	  "from_category":"consulting","to_category":"opex","applied":true}],
	  "parties":[],"fx_rates":[{"currency":"EUR","usd_rate":""}],"notes":[],"confidence":0.9}`

	fb, err := parseFacts(raw, FactsInput{ScenarioID: "P3", Company: "Acme"})
	if err != nil {
		t.Fatalf("a blank rate must not sink the fact base: %v", err)
	}
	if _, ok := fb.FXRates["EUR"]; ok {
		t.Error("a blank rate must not be recorded as a rate")
	}
	if len(fb.Adjustments) != 1 {
		t.Errorf("adjustments = %d, want the one the model did read", len(fb.Adjustments))
	}
}

func TestUnparseableFXRateStillFails(t *testing.T) {
	raw := `{"adjustments":[],"parties":[],"fx_rates":[{"currency":"EUR","usd_rate":"about 1.16"}],
	  "notes":[],"confidence":0.9}`
	if _, err := parseFacts(raw, FactsInput{ScenarioID: "P3"}); err == nil {
		t.Error("a garbled rate must be reported, not silently dropped")
	}
}
