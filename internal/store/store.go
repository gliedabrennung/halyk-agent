package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

const _schema = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS txns (
    txn_id       TEXT PRIMARY KEY,
    scenario_id  TEXT NOT NULL,
    account_id   TEXT NOT NULL,
    date         TEXT NOT NULL,
    counterparty TEXT NOT NULL,
    description  TEXT NOT NULL,
    amount       TEXT NOT NULL,
    currency     TEXT NOT NULL,
    amount_usd   TEXT NOT NULL DEFAULT '',
    amount_missing INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_txns_scenario ON txns(scenario_id);
CREATE INDEX IF NOT EXISTS idx_txns_account  ON txns(account_id);
CREATE INDEX IF NOT EXISTS idx_txns_cp       ON txns(counterparty);

CREATE TABLE IF NOT EXISTS documents (
    doc_id      TEXT PRIMARY KEY,
    path        TEXT NOT NULL,
    sha256      TEXT NOT NULL,
    bytes       INTEGER NOT NULL,
    pages       INTEGER NOT NULL,
    chars       INTEGER NOT NULL,
    empty_pages INTEGER NOT NULL DEFAULT 0,
    needs_ocr   INTEGER NOT NULL DEFAULT 0,
    ocr_used    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS pages (
    doc_id  TEXT NOT NULL,
    page_no INTEGER NOT NULL,
    text    TEXT NOT NULL,
    PRIMARY KEY (doc_id, page_no),
    FOREIGN KEY (doc_id) REFERENCES documents(doc_id) ON DELETE CASCADE
);

-- Full-text index over pages, used by the search_documents tool.
CREATE VIRTUAL TABLE IF NOT EXISTS pages_fts USING fts5(
    doc_id UNINDEXED, page_no UNINDEXED, text,
    content='pages', content_rowid='rowid'
);

CREATE TABLE IF NOT EXISTS llm_cache (
    key        TEXT PRIMARY KEY,
    model      TEXT NOT NULL,
    response   TEXT NOT NULL,
    tokens_in  INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

-- Generic artifact blobs (doc_index, specs, facts, labels) keyed by kind+id.
CREATE TABLE IF NOT EXISTS artifacts (
    kind       TEXT NOT NULL,
    id         TEXT NOT NULL,
    payload    TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (kind, id)
);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(_schema + _verdictSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply _schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

var _migrations = []struct{ table, column, ddl string }{
	{"txns", "amount_missing", `ALTER TABLE txns ADD COLUMN amount_missing INTEGER NOT NULL DEFAULT 0`},
	{"documents", "empty_pages", `ALTER TABLE documents ADD COLUMN empty_pages INTEGER NOT NULL DEFAULT 0`},
}

func migrate(db *sql.DB) error {
	for _, m := range _migrations {
		has, err := hasColumn(db, m.table, m.column)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", m.table, err)
		}
		if has {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", m.table, m.column, err)
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) ReplaceTxns(txns []domain.Txn) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM txns`); err != nil {
		return fmt.Errorf("clear txns: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO txns
        (txn_id, scenario_id, account_id, date, counterparty, description, amount, currency, amount_usd, amount_missing)
        VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range txns {
		_, err := stmt.Exec(t.ID, t.ScenarioID, t.AccountID, t.Date.Format("2006-01-02"),
			t.Counterparty, t.Description, t.Amount.String(), t.Currency, t.AmountUSD.String(),
			boolToInt(t.AmountMissing))
		if err != nil {
			return fmt.Errorf("insert %s: %w", t.ID, err)
		}
	}
	return tx.Commit()
}

func (s *Store) LoadTxns() ([]domain.Txn, error) {
	rows, err := s.db.Query(`SELECT txn_id, scenario_id, account_id, date, counterparty,
        description, amount, currency, amount_usd, amount_missing FROM txns ORDER BY txn_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Txn
	for rows.Next() {
		var t domain.Txn
		var date, amount, amountUSD string
		var amountMissing int
		if err := rows.Scan(&t.ID, &t.ScenarioID, &t.AccountID, &date, &t.Counterparty,
			&t.Description, &amount, &t.Currency, &amountUSD, &amountMissing); err != nil {
			return nil, err
		}
		t.AmountMissing = amountMissing != 0
		if t.Date, err = time.Parse("2006-01-02", date); err != nil {
			return nil, fmt.Errorf("txn %s: bad date %q: %w", t.ID, date, err)
		}
		if t.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, fmt.Errorf("txn %s: bad amount %q: %w", t.ID, amount, err)
		}
		if amountUSD != "" {
			if t.AmountUSD, err = decimal.NewFromString(amountUSD); err != nil {
				return nil, fmt.Errorf("txn %s: bad amount_usd %q: %w", t.ID, amountUSD, err)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpsertDocument(doc domain.Document, pages []domain.Page) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT INTO documents (doc_id, path, sha256, bytes, pages, chars, empty_pages, needs_ocr, ocr_used)
        VALUES (?,?,?,?,?,?,?,?,?)
        ON CONFLICT(doc_id) DO UPDATE SET
            path=excluded.path, sha256=excluded.sha256, bytes=excluded.bytes,
            pages=excluded.pages, chars=excluded.chars, empty_pages=excluded.empty_pages,
            needs_ocr=excluded.needs_ocr, ocr_used=excluded.ocr_used`,
		doc.ID, doc.Path, doc.SHA256, doc.Bytes, doc.Pages, doc.Chars, doc.EmptyPages,
		boolToInt(doc.NeedsOCR), boolToInt(doc.OCRUsed))
	if err != nil {
		return fmt.Errorf("upsert document %s: %w", doc.ID, err)
	}

	if _, err := tx.Exec(`DELETE FROM pages_fts WHERE doc_id = ?`, doc.ID); err != nil {
		return fmt.Errorf("clear fts %s: %w", doc.ID, err)
	}
	if _, err := tx.Exec(`DELETE FROM pages WHERE doc_id = ?`, doc.ID); err != nil {
		return fmt.Errorf("clear pages %s: %w", doc.ID, err)
	}
	stmt, err := tx.Prepare(`INSERT INTO pages (doc_id, page_no, text) VALUES (?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	ftsStmt, err := tx.Prepare(`INSERT INTO pages_fts (doc_id, page_no, text) VALUES (?,?,?)`)
	if err != nil {
		return err
	}
	defer ftsStmt.Close()

	for _, p := range pages {
		if _, err := stmt.Exec(p.DocID, p.No, p.Text); err != nil {
			return fmt.Errorf("insert page %s:%d: %w", p.DocID, p.No, err)
		}
		if _, err := ftsStmt.Exec(p.DocID, p.No, p.Text); err != nil {
			return fmt.Errorf("index page %s:%d: %w", p.DocID, p.No, err)
		}
	}
	return tx.Commit()
}

func (s *Store) DocumentSHA(docID string) (string, error) {
	var sha string
	err := s.db.QueryRow(`SELECT sha256 FROM documents WHERE doc_id = ?`, docID).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sha, err
}

func (s *Store) LoadDocuments() ([]domain.Document, error) {
	rows, err := s.db.Query(`SELECT doc_id, path, sha256, bytes, pages, chars, empty_pages, needs_ocr, ocr_used
        FROM documents ORDER BY doc_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Document
	for rows.Next() {
		var d domain.Document
		var needsOCR, ocrUsed int
		if err := rows.Scan(&d.ID, &d.Path, &d.SHA256, &d.Bytes, &d.Pages, &d.Chars, &d.EmptyPages, &needsOCR, &ocrUsed); err != nil {
			return nil, err
		}
		d.NeedsOCR, d.OCRUsed = needsOCR != 0, ocrUsed != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) PageText(docID string, page int) (string, error) {
	var text string
	err := s.db.QueryRow(`SELECT text FROM pages WHERE doc_id = ? AND page_no = ?`, docID, page).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no page %d in document %s", page, docID)
	}
	return text, err
}

func (s *Store) DocText(docID string) (string, error) {
	rows, err := s.db.Query(`SELECT text FROM pages WHERE doc_id = ? ORDER BY page_no`, docID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var b []byte
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return "", err
		}
		if len(b) > 0 {
			b = append(b, '\f')
		}
		b = append(b, t...)
	}
	return string(b), rows.Err()
}

func (s *Store) PutArtifact(kind, id string, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal artifact %s/%s: %w", kind, id, err)
	}
	_, err = s.db.Exec(`INSERT INTO artifacts (kind, id, payload, updated_at) VALUES (?,?,?,?)
        ON CONFLICT(kind, id) DO UPDATE SET payload=excluded.payload, updated_at=excluded.updated_at`,
		kind, id, string(payload), time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetArtifact(kind, id string, v any) (bool, error) {
	var payload string
	err := s.db.QueryRow(`SELECT payload FROM artifacts WHERE kind = ? AND id = ?`, kind, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(payload), v); err != nil {
		return false, fmt.Errorf("unmarshal artifact %s/%s: %w", kind, id, err)
	}
	return true, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
