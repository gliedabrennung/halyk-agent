package ingest

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

var _scenarioRe = regexp.MustCompile(`^TXN-([A-Za-z0-9]+)-\d+$`)

func ScenarioIDFromTxnID(txnID string) (string, error) {
	m := _scenarioRe.FindStringSubmatch(txnID)
	if m == nil {
		return "", fmt.Errorf("txn id %q does not match %s", txnID, _scenarioRe)
	}
	return m[1], nil
}

var _requiredLedgerColumns = []string{"txn_id", "date", "account_id", "counterparty", "description", "amount", "currency"}

func ParseLedger(path string) (*domain.Ledger, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()
	return ParseLedgerReader(f)
}

func ParseLedgerReader(r io.Reader) (*domain.Ledger, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.TrimSpace(strings.ToLower(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	for _, want := range _requiredLedgerColumns {
		if _, ok := col[want]; !ok {
			return nil, fmt.Errorf("ledger is missing column %q (header: %v)", want, header)
		}
	}

	var txns []domain.Txn
	seenTxnIDs := make(map[string]bool)

	line := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(rec) < len(header) {
			return nil, fmt.Errorf("line %d: got %d fields, want %d", line, len(rec), len(header))
		}
		get := func(name string) string { return strings.TrimSpace(rec[col[name]]) }

		t := domain.Txn{
			ID:           get("txn_id"),
			AccountID:    get("account_id"),
			Counterparty: get("counterparty"),
			Description:  get("description"),
			Currency:     strings.ToUpper(get("currency")),
		}
		if t.ID == "" {
			return nil, fmt.Errorf("line %d: empty txn_id", line)
		}
		if t.ScenarioID, err = ScenarioIDFromTxnID(t.ID); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if t.Date, err = time.Parse("2006-01-02", get("date")); err != nil {
			return nil, fmt.Errorf("line %d (%s): bad date %q: %w", line, t.ID, get("date"), err)
		}

		if raw := get("amount"); raw == "" {
			t.Amount, t.AmountMissing = decimal.Zero, true
		} else if t.Amount, err = decimal.NewFromString(raw); err != nil {
			return nil, fmt.Errorf("line %d (%s): bad amount %q: %w", line, t.ID, raw, err)
		}
		if t.Currency == "" {
			return nil, fmt.Errorf("line %d (%s): empty currency", line, t.ID)
		}
		if seenTxnIDs[t.ID] {
			return nil, fmt.Errorf("line %d: duplicate txn_id %s", line, t.ID)
		}
		seenTxnIDs[t.ID] = true

		txns = append(txns, t)
	}

	return domain.NewLedger(txns), nil
}

type BijectionIssue struct {
	Kind       string   `json:"kind"`
	Key        string   `json:"key"`
	Values     []string `json:"values"`
	TxnSamples []string `json:"txn_samples,omitempty"`
}

func (b BijectionIssue) String() string {
	return fmt.Sprintf("%s: %s -> %s", b.Kind, b.Key, strings.Join(b.Values, ", "))
}

func CheckBijection(led *domain.Ledger) []BijectionIssue {
	var issues []BijectionIssue

	byAccount := make(map[string]map[string][]string)
	for i := range led.Txns {
		t := &led.Txns[i]
		if byAccount[t.AccountID] == nil {
			byAccount[t.AccountID] = make(map[string][]string)
		}
		byAccount[t.AccountID][t.ScenarioID] = append(byAccount[t.AccountID][t.ScenarioID], t.ID)
	}
	accounts := domain.SortedKeys(byAccount)
	for _, acc := range accounts {
		if len(byAccount[acc]) <= 1 {
			continue
		}
		scns := domain.SortedKeys(byAccount[acc])
		samples := make([]string, 0, len(scns))
		for _, s := range scns {
			samples = append(samples, byAccount[acc][s][0])
		}
		issues = append(issues, BijectionIssue{
			Kind: "account_multi_scenario", Key: acc, Values: scns, TxnSamples: samples,
		})
	}

	for _, scn := range domain.SortedKeys(led.ScnToAccount) {
		if len(led.ScnToAccount[scn]) > 1 {
			issues = append(issues, BijectionIssue{
				Kind: "scenario_multi_account", Key: scn, Values: led.ScnToAccount[scn],
			})
		}
	}
	return issues
}
