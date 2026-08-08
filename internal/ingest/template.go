package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/store"
)

var _cellFields = []string{"status", "actual", "evidence_txn_id"}

func ParseTemplate(path string) (*domain.Template, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open template: %w", err)
	}
	t, err := parseTemplateBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

func parseTemplateBytes(raw []byte) (*domain.Template, error) {
	var top struct {
		Team         string `json:"team"`
		ContactEmail string `json:"contact_email"`
		Model        string `json:"model"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	tpl := &domain.Template{
		Team:         top.Team,
		ContactEmail: top.ContactEmail,
		Model:        top.Model,
		Raw:          raw,
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	seenAnswers := false
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return nil, err
		}
		if key != "answers" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("skip %q: %w", key, err)
			}
			continue
		}
		seenAnswers = true
		if err := expectDelim(dec, '{'); err != nil {
			return nil, fmt.Errorf("answers: %w", err)
		}
		for dec.More() {
			scenario, err := readKey(dec)
			if err != nil {
				return nil, err
			}
			tpl.Scenarios = append(tpl.Scenarios, scenario)
			if err := expectDelim(dec, '{'); err != nil {
				return nil, fmt.Errorf("answers.%s: %w", scenario, err)
			}
			for dec.More() {
				clause, err := readKey(dec)
				if err != nil {
					return nil, err
				}
				var cell map[string]json.RawMessage
				if err := dec.Decode(&cell); err != nil {
					return nil, fmt.Errorf("answers.%s.%s: %w", scenario, clause, err)
				}
				if err := checkCellShape(scenario, clause, cell); err != nil {
					return nil, err
				}
				tpl.Cells = append(tpl.Cells, domain.Cell{ScenarioID: scenario, ClauseID: clause})
			}
			if err := expectDelim(dec, '}'); err != nil {
				return nil, fmt.Errorf("answers.%s: %w", scenario, err)
			}
		}
		if err := expectDelim(dec, '}'); err != nil {
			return nil, fmt.Errorf("answers: %w", err)
		}
	}
	if !seenAnswers {
		return nil, fmt.Errorf("template has no \"answers\" object")
	}
	if len(tpl.Cells) == 0 {
		return nil, fmt.Errorf("template has no answer cells")
	}
	return tpl, nil
}

func checkCellShape(scenario, clause string, cell map[string]json.RawMessage) error {
	for _, f := range _cellFields {
		if _, ok := cell[f]; !ok {
			return fmt.Errorf("answers.%s.%s is missing field %q", scenario, clause, f)
		}
	}
	if len(cell) != len(_cellFields) {
		extra := make([]string, 0, len(cell))
		for k := range cell {
			if !contains(_cellFields, k) {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		return fmt.Errorf("answers.%s.%s has unexpected fields %v", scenario, clause, extra)
	}
	return nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

func readKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected object key, got %v", tok)
	}
	return s, nil
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func LoadTemplateAndTxns(cfg *config.Config, st *store.Store) (*domain.Template, []domain.Txn, error) {
	tpl, err := ParseTemplate(cfg.TemplatePath())
	if err != nil {
		return nil, nil, err
	}
	txns, err := st.LoadTxns()
	if err != nil {
		return nil, nil, err
	}
	if len(txns) == 0 {
		return nil, nil, fmt.Errorf("no transactions in the store; run `halyk-agent ingest` first")
	}
	return tpl, txns, nil
}
