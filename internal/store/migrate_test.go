package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Старая схема без amount_missing/empty_pages — проверяем, что migrate их добавит.
func TestMigrateAddsMissingColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE txns (txn_id TEXT PRIMARY KEY);
		CREATE TABLE documents (doc_id TEXT PRIMARY KEY);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ table, col string }{{"txns", "amount_missing"}, {"documents", "empty_pages"}} {
		if has, err := hasColumn(db, c.table, c.col); err != nil || has {
			t.Fatalf("hasColumn(%s,%s) = %v, %v; want false, nil", c.table, c.col, has, err)
		}
	}
	if has, err := hasColumn(db, "txns", "txn_id"); err != nil || !has {
		t.Fatalf("hasColumn(txns,txn_id) = %v, %v; want true, nil", has, err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ table, col string }{{"txns", "amount_missing"}, {"documents", "empty_pages"}} {
		if has, err := hasColumn(db, c.table, c.col); err != nil || !has {
			t.Fatalf("after migrate hasColumn(%s,%s) = %v, %v; want true, nil", c.table, c.col, has, err)
		}
	}
	db.Close()
}
