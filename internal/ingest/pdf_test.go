package ingest

import (
	"strings"
	"testing"
)

func TestDocIDFromPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"data/documents/04eee2e9ba8c.pdf", "04eee2e9ba8c"},
		{"/abs/path/ab12.PDF", "ab12"},
		{"noext", "noext"},
	}
	for _, tt := range tests {
		if got := DocIDFromPath(tt.in); got != tt.want {
			t.Errorf("DocIDFromPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCountPrintable(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   \n\t\f ", 0},
		{"abc", 3},
		{"a b\nc", 3},
		{"Пункт 6.2", 8},
	}
	for _, tt := range tests {
		if got := countPrintable(tt.in); got != tt.want {
			t.Errorf("countPrintable(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestBuildDocument(t *testing.T) {
	pages := []string{"Credit Agreement\nACC-7801", "6.2 Capital Expenditure shall not exceed $5,000,000."}
	doc, docPages := buildDocument("/data/documents/abc123.pdf", "deadbeef", 4096, pages)

	if doc.ID != "abc123" {
		t.Errorf("id = %q, want abc123", doc.ID)
	}
	if doc.Pages != 2 {
		t.Errorf("pages = %d, want 2", doc.Pages)
	}
	if doc.SHA256 != "deadbeef" || doc.Bytes != 4096 {
		t.Errorf("sha/bytes = %q/%d", doc.SHA256, doc.Bytes)
	}
	if doc.NeedsOCR {
		t.Error("a document with real text must not be flagged for OCR")
	}
	if doc.Chars == 0 {
		t.Error("chars must be counted")
	}
	if len(docPages) != 2 {
		t.Fatalf("page records = %d, want 2", len(docPages))
	}
	if docPages[0].No != 1 || docPages[1].No != 2 {
		t.Errorf("page numbers must be 1-based: %d, %d", docPages[0].No, docPages[1].No)
	}
	if docPages[0].DocID != "abc123" {
		t.Errorf("page doc id = %q", docPages[0].DocID)
	}
	if !strings.Contains(docPages[1].Text, "Capital Expenditure") {
		t.Error("page text must be preserved verbatim")
	}
}

func TestBuildDocumentFlagsScannedPDF(t *testing.T) {

	doc, _ := buildDocument("/data/documents/scan.pdf", "sha", 100, []string{"", "  \n", "3"})
	if !doc.NeedsOCR {
		t.Error("a text-empty document must be flagged for OCR")
	}
}
