package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func CacheKey(model, prompt, schemaVersion string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", model, schemaVersion, prompt)
	return hex.EncodeToString(h.Sum(nil))
}

type LLMCallLog struct {
	Key       string    `json:"key"`
	Model     string    `json:"model"`
	Prompt    string    `json:"prompt"`
	Response  string    `json:"response"`
	Reasoning string    `json:"reasoning,omitempty"`
	TokensIn  int       `json:"tokens_in"`
	TokensOut int       `json:"tokens_out"`
	LatencyMS int64     `json:"latency_ms"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) GetCached(key string) (string, bool, error) {
	var resp string
	err := s.db.QueryRow(`SELECT response FROM llm_cache WHERE key = ?`, key).Scan(&resp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return resp, true, nil
}

func (s *Store) PutCached(key, model, response string, tokensIn, tokensOut int, latency time.Duration) error {
	_, err := s.db.Exec(`INSERT INTO llm_cache (key, model, response, tokens_in, tokens_out, latency_ms, created_at)
        VALUES (?,?,?,?,?,?,?)
        ON CONFLICT(key) DO UPDATE SET response=excluded.response, tokens_in=excluded.tokens_in,
            tokens_out=excluded.tokens_out, latency_ms=excluded.latency_ms, created_at=excluded.created_at`,
		key, model, response, tokensIn, tokensOut, latency.Milliseconds(), time.Now().UTC().Format(time.RFC3339))
	return err
}

func DumpLLMCall(logDir string, rec LLMCallLog) error {
	dir := filepath.Join(logDir, "llm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rec.Key+".json"), b, 0o644)
}

func (s *Store) CacheStats() (count, tokensIn, tokensOut int, err error) {
	row := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0) FROM llm_cache`)
	err = row.Scan(&count, &tokensIn, &tokensOut)
	return
}
