package facts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/index"
	"github.com/gliedabrennung/halyk-agent/internal/store"
)

func testStore(t *testing.T, docs map[string][]string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	for id, pages := range docs {
		doc := domain.Document{ID: id, Path: id + ".pdf", SHA256: id, Pages: len(pages)}
		rows := make([]domain.Page, 0, len(pages))
		for i, text := range pages {
			doc.Chars += len(text)
			rows = append(rows, domain.Page{DocID: id, No: i + 1, Text: text})
		}
		if err := st.UpsertDocument(doc, rows); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	return st
}

func entry(docID string, docType domain.DocType, scenario string) index.Entry {
	return index.Entry{
		DocID:      docID,
		Path:       docID + ".pdf",
		Pages:      1,
		DocType:    docType,
		ScenarioID: scenario,
		Effective:  true,
		Scan:       index.Scan{Chars: 5000},
	}
}

func buildFor(t *testing.T, st *store.Store, idx *index.Index, scenario string) (agents.FactsInput, error) {
	t.Helper()
	opts := Options{Store: st, Cfg: &config.Config{CacheDir: t.TempDir()}}
	return buildInput(t.Context(), opts, idx, scenario)
}

// A borrower's file is assembled from documents whose type always matters plus
// any document that names one of its transactions. Anything belonging to
// another borrower must stay out — a stray audit report would carry another
// company's adjustments into this covenant.
func TestBuildInputPicksDocumentsByTypeAndByTransactionReference(t *testing.T) {
	st := testStore(t, map[string][]string{
		"audit":     {"Аудиторское заключение по P1."},
		"memo":      {"Служебная записка, касается TXN-P1-0045 и ничего более."},
		"unrelated": {"Записка про TXN-P9-0001, чужой заёмщик."},
		"otheraud":  {"Аудит другого заёмщика."},
	})
	idx := &index.Index{
		Entries: []index.Entry{
			entry("audit", domain.DocAuditReport, "P1"),
			entry("memo", domain.DocOther, "P1"),
			entry("unrelated", domain.DocOther, "P1"),
			entry("otheraud", domain.DocAuditReport, "P9"),
		},
		GroupParents: map[string]string{},
	}

	in, err := buildFor(t, st, idx, "P1")
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}

	var got []string
	for _, d := range in.Documents {
		got = append(got, d.DocID)
	}
	want := []string{"audit", "memo"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("documents = %v, want %v", got, want)
	}
}

// An ineffective document is a superseded revision. Reading it would apply an
// adjustment the auditor has already withdrawn.
func TestBuildInputIgnoresSupersededDocuments(t *testing.T) {
	st := testStore(t, map[string][]string{
		"current": {"Действующее заключение."},
		"old":     {"Устаревшая редакция."},
	})
	stale := entry("old", domain.DocAuditReport, "P1")
	stale.Effective = false

	idx := &index.Index{
		Entries:      []index.Entry{entry("current", domain.DocAuditReport, "P1"), stale},
		GroupParents: map[string]string{},
	}

	in, err := buildFor(t, st, idx, "P1")
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	if len(in.Documents) != 1 || in.Documents[0].DocID != "current" {
		t.Errorf("documents = %+v, want only the effective one", in.Documents)
	}
}

// The parent's consolidated statements are the only place a group figure
// exists, and they are attached apart from the borrower's own file so that the
// prompt can say they describe the group, not this borrower.
func TestBuildInputAttachesTheGroupParentSeparately(t *testing.T) {
	st := testStore(t, map[string][]string{
		"audit":  {"Заключение по P5."},
		"parent": {"Consolidated statements of the parent."},
	})
	idx := &index.Index{
		Entries:      []index.Entry{entry("audit", domain.DocAuditReport, "P5")},
		GroupParents: map[string]string{"P5": "parent"},
	}

	in, err := buildFor(t, st, idx, "P5")
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	if in.GroupDoc == nil {
		t.Fatal("the consolidated statements must be attached")
	}
	if in.GroupDoc.DocID != "parent" {
		t.Errorf("GroupDoc = %q, want parent", in.GroupDoc.DocID)
	}
	for _, d := range in.Documents {
		if d.DocID == "parent" {
			t.Error("the parent's statements must not also count as the borrower's own document")
		}
	}
}

// A borrower with nothing to read is an error, not an empty fact base: silently
// returning nothing would report every disclosure as absent.
func TestBuildInputRefusesABorrowerWithNoReadableFile(t *testing.T) {
	st := testStore(t, map[string][]string{"other": {"Чужой документ."}})
	idx := &index.Index{
		Entries:      []index.Entry{entry("other", domain.DocAuditReport, "P2")},
		GroupParents: map[string]string{},
	}

	if _, err := buildFor(t, st, idx, "P1"); err == nil {
		t.Error("want an error naming the borrower with no documents")
	}
}

func TestEmptyPagesFindsThePagesWithoutATextLayer(t *testing.T) {
	st := testStore(t, map[string][]string{
		"doc": {
			strings.Repeat("текст ", 40),
			"",
			"   ",
			"две слова",
			strings.Repeat("ещё ", 40),
		},
	})

	pages, err := emptyPages(st, "doc")
	if err != nil {
		t.Fatalf("emptyPages: %v", err)
	}
	want := []int{2, 3, 4}
	if len(pages) != len(want) {
		t.Fatalf("pages = %v, want %v", pages, want)
	}
	for i := range want {
		if pages[i] != want[i] {
			t.Fatalf("pages = %v, want %v", pages, want)
		}
	}
}

func TestOCRPageCountAddsUpEveryDocument(t *testing.T) {
	var empty agents.FactsInput
	if got := ocrPageCount(empty); got != 0 {
		t.Errorf("ocrPageCount(zero) = %d, want 0", got)
	}

	in := agents.FactsInput{Documents: []agents.FactsDoc{
		{OCRPages: []int{1, 2}},
		{},
		{OCRPages: []int{7}},
	}}
	if got := ocrPageCount(in); got != 3 {
		t.Errorf("ocrPageCount = %d, want 3", got)
	}
}

func TestTruncateMarksWhatItCut(t *testing.T) {
	tests := []struct {
		name string
		give string
		n    int
		want string
	}{
		{name: "shorter than the limit", give: "abc", n: 10, want: "abc"},
		{name: "exactly the limit", give: "abcde", n: 5, want: "abcde"},
		{name: "cut", give: "abcdefgh", n: 3, want: "abc\n[... truncated ...]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.give, tt.n); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.give, tt.n, got, tt.want)
			}
		})
	}
}
