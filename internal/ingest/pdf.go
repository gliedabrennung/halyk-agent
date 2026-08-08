package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

const (
	binPDFToText = "pdftotext"
	binPDFToPPM  = "pdftoppm"
	binTesseract = "tesseract"
)

func CheckTools(needRender bool) error {
	var missing []string
	if _, err := exec.LookPath(binPDFToText); err != nil {
		missing = append(missing, binPDFToText)
	}
	if needRender {
		if _, err := exec.LookPath(binPDFToPPM); err != nil {
			missing = append(missing, binPDFToPPM)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required binaries: %s (install poppler-utils)", strings.Join(missing, ", "))
	}
	return nil
}

func HasTesseract() bool {
	_, err := exec.LookPath(binTesseract)
	return err == nil
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

func ExtractText(ctx context.Context, pdfPath string) ([]string, error) {
	out, err := run(ctx, binPDFToText, "-layout", "-enc", "UTF-8", pdfPath, "-")
	if err != nil {
		return nil, fmt.Errorf("pdftotext %s: %w", filepath.Base(pdfPath), err)
	}

	pages := strings.Split(out, "\f")

	if n := len(pages); n > 0 && strings.TrimSpace(pages[n-1]) == "" {
		pages = pages[:n-1]
	}
	if len(pages) == 0 {
		pages = []string{""}
	}
	return pages, nil
}

func RenderPagePNG(ctx context.Context, pdfPath string, page, dpi int, cacheDir string) ([]byte, error) {
	if dpi <= 0 {
		dpi = 150
	}
	docID := DocIDFromPath(pdfPath)
	dir := filepath.Join(cacheDir, "png", docID)
	target := filepath.Join(dir, fmt.Sprintf("p%04d-%d.png", page, dpi))

	if b, err := os.ReadFile(target); err == nil && len(b) > 0 {
		return b, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	prefix := filepath.Join(dir, fmt.Sprintf("tmp-p%04d-%d", page, dpi))
	if _, err := run(ctx, binPDFToPPM, "-png", "-r", fmt.Sprint(dpi),
		"-f", fmt.Sprint(page), "-l", fmt.Sprint(page), pdfPath, prefix); err != nil {
		return nil, fmt.Errorf("pdftoppm %s p%d: %w", docID, page, err)
	}

	matches, err := filepath.Glob(prefix + "*.png")
	if err != nil || len(matches) == 0 {
		return nil, fmt.Errorf("pdftoppm %s p%d produced no output", docID, page)
	}
	if err := os.Rename(matches[0], target); err != nil {
		return nil, err
	}
	for _, m := range matches[1:] {
		os.Remove(m)
	}
	return os.ReadFile(target)
}

func OCRPage(ctx context.Context, pdfPath string, page int, cacheDir string) (string, error) {
	const ocrLanguages = "rus+eng+kaz"
	if !HasTesseract() {
		return "", fmt.Errorf("%s is not installed but %s page %d has no text layer; install tesseract (%s)",
			binTesseract, filepath.Base(pdfPath), page, ocrLanguages)
	}
	png, err := RenderPagePNG(ctx, pdfPath, page, 300, cacheDir)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "halyk-ocr-*.png")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(png); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()

	out, err := run(ctx, binTesseract, tmp.Name(), "stdout", "-l", ocrLanguages, "--psm", "6")
	if err != nil {
		return "", fmt.Errorf("tesseract %s p%d: %w", filepath.Base(pdfPath), page, err)
	}
	return out, nil
}

func DocIDFromPath(p string) string {
	return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
}

func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

var _textExtensions = map[string]bool{".txt": true, ".csv": true, ".md": true, ".json": true}

func ListDocumentFiles(dir string) (files []string, skipped []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read documents dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			skipped = append(skipped, e.Name()+"/")
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".pdf" || _textExtensions[ext] {
			files = append(files, filepath.Join(dir, e.Name()))
			continue
		}
		skipped = append(skipped, e.Name())
	}
	return files, skipped, nil
}

func IsPDF(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".pdf")
}

func ExtractFile(ctx context.Context, path string) ([]string, error) {
	if IsPDF(path) {
		return ExtractText(ctx, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("%s is not valid UTF-8", filepath.Base(path))
	}
	return []string{string(b)}, nil
}

func countPrintable(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

func buildDocument(path, sha string, size int64, pages []string) (domain.Document, []domain.Page) {
	const minCharsPerPage = 40
	docID := DocIDFromPath(path)
	doc := domain.Document{
		ID:     docID,
		Path:   path,
		SHA256: sha,
		Bytes:  size,
		Pages:  len(pages),
	}
	out := make([]domain.Page, 0, len(pages))
	empty := 0
	for i, text := range pages {
		n := countPrintable(text)
		doc.Chars += n
		if n < minCharsPerPage {
			empty++
		}
		out = append(out, domain.Page{DocID: docID, No: i + 1, Text: text})
	}
	doc.EmptyPages = empty
	if len(pages) > 0 && empty*2 > len(pages) {
		doc.NeedsOCR = true
	}
	return doc, out
}
