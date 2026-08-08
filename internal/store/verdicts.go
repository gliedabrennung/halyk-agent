package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

const _verdictSchema = `
CREATE TABLE IF NOT EXISTS verdicts (
    scenario_id TEXT NOT NULL,
    clause_id   TEXT NOT NULL,
    status      TEXT NOT NULL,
    actual      TEXT NOT NULL,
    evidence    TEXT,
    source      TEXT NOT NULL DEFAULT '',
    confidence  REAL NOT NULL DEFAULT 0,
    payload     TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (scenario_id, clause_id)
);
`

func (s *Store) PutVerdict(v domain.Verdict) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal verdict %s/%s: %w", v.ScenarioID, v.ClauseID, err)
	}
	var evidence any
	if v.EvidenceID != nil {
		evidence = *v.EvidenceID
	}
	_, err = s.db.Exec(`INSERT INTO verdicts
        (scenario_id, clause_id, status, actual, evidence, source, confidence, payload, updated_at)
        VALUES (?,?,?,?,?,?,?,?,?)
        ON CONFLICT(scenario_id, clause_id) DO UPDATE SET
            status=excluded.status, actual=excluded.actual, evidence=excluded.evidence,
            source=excluded.source, confidence=excluded.confidence,
            payload=excluded.payload, updated_at=excluded.updated_at`,
		v.ScenarioID, v.ClauseID, v.Status, v.Actual.String(), evidence, v.Source, v.Confidence,
		string(payload), time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) LoadVerdicts() (map[string]domain.Verdict, error) {
	rows, err := s.db.Query(`SELECT payload FROM verdicts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]domain.Verdict)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var v domain.Verdict
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			return nil, fmt.Errorf("unmarshal verdict: %w", err)
		}
		out[VerdictKey(v.ScenarioID, v.ClauseID)] = v
	}
	return out, rows.Err()
}

func VerdictKey(scenarioID, clauseID string) string { return scenarioID + "/" + clauseID }
