package report

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/shopspring/decimal"
)

type Options struct {
	Cfg   *config.Config
	Store *store.Store
	Log   *slog.Logger

	Only []string

	Path string
}

type Report struct {
	Duration time.Duration `json:"duration"`
	Path     string        `json:"path"`
	Bytes    int64         `json:"bytes"`
	Pages    int           `json:"pages"`

	Borrowers int `json:"borrowers"`
	Cells     int `json:"cells"`
	Breaches  int `json:"breaches"`
	Flagged   int `json:"flagged"`

	Warnings []string `json:"warnings,omitempty"`
}

func Run(opts Options) (*Report, error) {
	start := time.Now()

	d, err := collect(opts)
	if err != nil {
		return nil, err
	}

	path := opts.Path
	if path == "" {
		path = filepath.Join(opts.Cfg.OutDir, "report.pdf")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	r := newRenderer("Covenant compliance report")
	render(r, d)
	if err := r.pdf.Error(); err != nil {
		return nil, fmt.Errorf("render report: %w", err)
	}
	pages := r.pdf.PageNo()
	if err := r.pdf.OutputFileAndClose(path); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}

	rep := &Report{
		Duration:  time.Since(start),
		Path:      path,
		Pages:     pages,
		Borrowers: d.Corpus.Borrowers,
		Cells:     d.Corpus.Cells,
		Breaches:  d.Corpus.Breach,
		Flagged:   d.Corpus.Flagged,
		Warnings:  d.Warnings,
	}
	if info, err := os.Stat(path); err == nil {
		rep.Bytes = info.Size()
	}
	if opts.Log != nil {
		opts.Log.Info("wrote report", "path", path, "pages", pages, "cells", rep.Cells)
	}
	return rep, nil
}

func (r *Report) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 104)
	fmt.Fprintf(&b, "\n%s\nREPORT  (%.1fs)  %d pages, %d borrowers, %d cells, %d breaches, %d flagged for review\n%s\n",
		line, r.Duration.Seconds(), r.Pages, r.Borrowers, r.Cells, r.Breaches, r.Flagged, line)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "  %s\n", w)
	}
	fmt.Fprintf(&b, "wrote %s (%.0f KiB)\n%s\n", r.Path, float64(r.Bytes)/1024, line)
	return b.String()
}

func render(r *renderer, d *dossier) {
	cover(r, d)
	summary(r, d)
	for i := range d.Borrowers {
		borrowerSection(r, &d.Borrowers[i])
	}
	appendix(r, d)
}

func cover(r *renderer, d *dossier) {
	r.pdf.AddPage()
	r.space(14)

	r.font("", 10)
	r.textColor(colMuted)
	r.pdf.CellFormat(contentWidth, 6, "Halyk AI Challenge", "", 1, "L", false, 0, "")
	r.textColor(colText)

	r.font("B", 26)
	r.textColor(colAccent)
	r.pdf.MultiCell(contentWidth, 12, "Covenant compliance report", "", "L", false)
	r.textColor(colText)
	r.space(2)
	r.rule()
	r.space(4)

	r.tiles([][2]string{
		{strconv.Itoa(d.Corpus.Borrowers), "borrowers"},
		{strconv.Itoa(d.Corpus.Cells), "covenants"},
		{strconv.Itoa(d.Corpus.Breach), "in breach"},
		{strconv.Itoa(d.Corpus.WithEvidence), "with evidence"},
		{strconv.Itoa(d.Corpus.Flagged), "to check"},
	})
	r.space(4)

	r.kv("generated", d.Generated.Format("2006-01-02 15:04:05 UTC"))
	r.kv("submission", submissionLabel(d))
	r.kv("corpus", fmt.Sprintf("%d documents, %d pages, %d ledger transactions",
		d.Corpus.Documents, d.Corpus.Pages, d.Corpus.Txns))
	r.kv("engine cells", fmt.Sprintf("%d of %d answered by the deterministic engine",
		d.Corpus.Reasoned, d.Corpus.Cells))
	r.space(6)

	r.heading2("How these answers were produced")
	r.para("Language models read the corpus; they never decide. Every clause is extracted as an " +
		"executable specification — an arithmetic expression over named terms, with a threshold, an " +
		"operator, a period, carve-outs and a trigger. Auditor adjustments, the ownership graph and " +
		"the exchange rates are extracted as a fact base, and every ledger row is given a category " +
		"and a related-party flag.")
	r.para("A deterministic Go engine then applies each specification to the labelled ledger: it " +
		"computes the metric in exact decimal arithmetic, evaluates the trigger, applies the " +
		"carve-outs, compares the result with the threshold, and picks the evidence transaction by " +
		"leave-one-out — the single row whose removal flips the verdict. Each finished cell is read " +
		"back against its clause by a critic; disagreements are reported here rather than hidden.")
	r.para("Every figure in this report is traceable: each covenant below shows the metric that was " +
		"computed, the clause text it came from, and the engine's own computation trace.")
}

func summary(r *renderer, d *dossier) {
	r.pdf.AddPage()
	r.pdf.Bookmark("Summary", 0, -1)
	r.heading1("Summary")
	r.muted(fmt.Sprintf("%d cells: %d compliant, %d in breach; %d name an evidence transaction; "+
		"the critic disagreed with %d.",
		d.Corpus.Cells, d.Corpus.Compliant, d.Corpus.Breach, d.Corpus.WithEvidence, d.Corpus.Disputed), 8.5)
	r.space(4)

	cols := []column{
		{"scn", 12, "L"},
		{"clause", 16, "L"},
		{"status", 24, "L"},
		{"actual", 34, "R"},
		{"evidence", 32, "L"},
		{"conf.", 12, "R"},
		{"note", 48, "L"},
	}
	r.tableHeader(cols)

	zebra := false
	for i := range d.Borrowers {
		b := &d.Borrowers[i]
		for j := range b.Cells {
			c := &b.Cells[j]
			status := statusColor(c.Status)
			r.tableRow(cols, []string{
				b.ScenarioID, c.ClauseID, c.Status, money(c.Actual), evidenceOf(c.Evidence),
				confidenceOf(c), noteOf(c),
			}, []*rgb{nil, nil, &status, nil, nil, nil, nil}, zebra)
			zebra = !zebra
		}
	}
	r.space(3)
	r.muted("Confidence is the engine's own: it falls when a term had to be inferred, when the "+
		"leave-one-out search found no single decisive row, or when the critic disagreed.", 7.5)
}

func borrowerSection(r *renderer, b *borrower) {
	r.pdf.AddPage()
	title := b.ScenarioID
	if b.Company != "" {
		title += " — " + b.Company
	}
	r.pdf.Bookmark(title, 0, -1)
	r.heading1(title)

	r.kv("accounts", strings.Join(b.Accounts, ", "))
	r.kv("ledger", fmt.Sprintf("%d transactions", b.Txns))
	if len(b.Related) > 0 {
		related := strings.Join(b.Related, ", ")
		if b.RelatedThreshold.IsPositive() {
			related += fmt.Sprintf("  (voting share threshold %s%%)", b.RelatedThreshold.String())
		}
		r.kv("related parties", related)
	}
	if len(b.FX) > 0 {
		var rates []string
		for _, cur := range domain.SortedKeys(b.FX) {
			rates = append(rates, fmt.Sprintf("%s %s", cur, b.FX[cur].String()))
		}
		r.kv("exchange rates", strings.Join(rates, ", "))
	}
	if len(b.Docs) > 0 {
		r.kv("documents", documentSummary(b.Docs))
	}
	for _, note := range b.FactNotes {
		r.kv("note", note)
	}
	r.space(3)

	adjustments(r, b)
	categoryTable(r, b)

	for i := range b.Cells {
		clauseSection(r, &b.Cells[i])
	}
}

func adjustments(r *renderer, b *borrower) {
	if len(b.Adjustments) == 0 {
		return
	}
	r.heading2("Auditor adjustments")
	cols := []column{
		{"kind", 32, "L"},
		{"transaction", 28, "L"},
		{"amount", 30, "R"},
		{"applied", 16, "L"},
		{"rationale", 72, "L"},
	}
	r.tableHeader(cols)
	zebra := false
	for _, a := range b.Adjustments {
		applied := "no"
		if a.Applied {
			applied = "yes"
		}
		rationale := a.Rationale
		if a.FromCategory != "" || a.ToCategory != "" {
			rationale = strings.TrimSpace(fmt.Sprintf("%s → %s. %s", a.FromCategory, a.ToCategory, rationale))
		}
		r.tableRow(cols, []string{
			a.Kind, a.TxnID, money(a.Amount), applied, collapse(rationale),
		}, nil, zebra)
		zebra = !zebra
	}
	r.space(4)
}

func categoryTable(r *renderer, b *borrower) {
	if len(b.Totals) == 0 {
		return
	}
	r.heading2("Labelled ledger")
	cols := []column{
		{"category", 78, "L"},
		{"rows", 30, "R"},
		{"total, USD", 70, "R"},
	}
	r.tableHeader(cols)
	zebra := false
	for _, t := range b.Totals {
		if t.Amount.IsZero() && t.Txns == 0 {
			continue
		}
		r.tableRow(cols, []string{
			string(t.Category), strconv.Itoa(t.Txns), money(t.Amount),
		}, nil, zebra)
		zebra = !zebra
	}
	r.space(4)
}

func clauseSection(r *renderer, c *cell) {
	r.need(60)
	r.pdf.Bookmark(c.ScenarioID+" "+c.ClauseID, 1, -1)
	clauseHeader(r, c)

	status := statusColor(c.Status)
	r.kvColored("status", c.Status, status)
	r.kv("actual", money(c.Actual))

	if c.Spec != nil {
		s := c.Spec
		r.kv("test", fmt.Sprintf("%s %s %s%s", s.Expression, s.Op, s.Threshold.String(), unitSuffix(s.Unit)))
		r.kv("period", periodOf(s.Period))
		if s.Trigger != nil && s.Trigger.Expression != "" {
			r.kv("trigger", strings.TrimSpace(s.Trigger.Expression+"  "+s.Trigger.Description))
		}
		if terms := termLines(s); terms != "" {
			r.kv("terms", terms)
		}
		for _, co := range s.Carveouts {
			r.kv("carve-out", strings.TrimSpace(co.Description+"  "+co.Condition.Expression))
		}
	} else {
		r.kv("specification", "none — this cell fell back to the baseline placeholder")
	}

	if c.Verdict != nil && c.Verdict.CarveoutApplied != "" {
		r.kv("carve-out applied", c.Verdict.CarveoutApplied)
	}
	r.kv("evidence", evidenceDetail(c))
	if conf := confidenceOf(c); conf != "" {
		r.kv("confidence", conf)
	}
	if critic := c.Critic(); critic != "" {
		r.kv("critic", critic)
	}

	if c.Spec != nil && c.Spec.SourceRef.Quote != "" {
		r.space(1)
		r.muted(sourceLabel(c), 7.5)
		r.quote(c.Spec.SourceRef.Quote)
	}
	if trace := c.Trace(); len(trace) > 0 {
		r.space(1.5)
		r.muted("computation", 7.5)
		r.trace(trace)
	}
	r.space(6)
}

func clauseHeader(r *renderer, c *cell) {
	y := r.pdf.GetY()
	r.badge(c.Status, statusColor(c.Status))

	title := c.ClauseID
	if c.Title != "" {
		title += " — " + c.Title
	}
	r.font("B", 10.5)
	r.pdf.SetXY(marginLeft, y)
	r.pdf.MultiCell(contentWidth-34, 5.6, clean(title), "", "L", false)
	if r.pdf.GetY() < y+6.2 {
		r.pdf.SetY(y + 6.2)
	}
	r.rule()
}

func appendix(r *renderer, d *dossier) {
	r.pdf.AddPage()
	r.pdf.Bookmark("Cells to check by hand", 0, -1)
	r.heading1("Cells to check by hand")
	r.space(2)

	var flagged []*cell
	for i := range d.Borrowers {
		for j := range d.Borrowers[i].Cells {
			c := &d.Borrowers[i].Cells[j]
			if c.Flagged() || c.Disputed() {
				flagged = append(flagged, c)
			}
		}
	}
	if len(flagged) == 0 {
		r.para("None. Every cell was computed from a specification the critic accepted, with every " +
			"term resolved to a disclosed figure.")
	}
	for _, c := range flagged {
		r.heading3(fmt.Sprintf("%s / %s — %s", c.ScenarioID, c.ClauseID, c.Title))
		r.kvColored("status", c.Status, statusColor(c.Status))
		r.kv("actual", money(c.Actual))
		r.kv("confidence", confidenceOf(c))
		r.kv("why", noteOf(c))
		if critic := c.Critic(); critic != "" {
			r.kv("critic", critic)
		}
		r.space(3)
	}

	if len(d.Warnings) > 0 {
		r.space(4)
		r.heading2("Warnings from this run")
		for _, w := range d.Warnings {
			r.para("· " + w)
		}
	}

	r.space(4)
	r.heading2("Reproducing this report")
	r.para("The pipeline is deterministic given the store: halyk-agent run executes every stage in " +
		"order, halyk-agent evaluate re-runs the engine alone, and halyk-agent report re-renders this " +
		"file from the stored verdicts and the submission. Model responses are cached by prompt hash, " +
		"so a re-run of an unchanged stage costs no calls.")
	r.kv("submission", submissionLabel(d))
	r.kv("generated", d.Generated.Format(time.RFC3339))
}

func submissionLabel(d *dossier) string {
	if d.SubmissionAge.IsZero() {
		return d.SubmissionPath
	}
	return fmt.Sprintf("%s  (written %s)", d.SubmissionPath, d.SubmissionAge.Format("2006-01-02 15:04:05 UTC"))
}

func statusColor(status string) rgb {
	switch status {
	case domain.StatusCompliant:
		return colCompliant
	case domain.StatusBreach:
		return colBreach
	default:
		return colMuted
	}
}

func money(d decimal.Decimal) string {
	s := d.StringFixed(2)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return sign + b.String() + "." + frac
}

func evidenceOf(id string) string {
	if id == "" {
		return "—"
	}
	return id
}

func evidenceDetail(c *cell) string {
	if c.Evidence == "" {
		kind := "the clause is not decided by a single transaction"
		if c.Spec != nil && c.Spec.EvidenceKind != "" {
			kind = "evidence kind: " + c.Spec.EvidenceKind
		}
		return "none — " + kind
	}
	if t := c.EvidenceTxn; t != nil {
		return fmt.Sprintf("%s\n%s  %s  %s %s\n%s",
			c.Evidence, t.Date.Format("2006-01-02"), t.Counterparty,
			money(t.Amount), strings.ToUpper(t.Currency), collapse(t.Description))
	}
	return c.Evidence
}

func confidenceOf(c *cell) string {
	if c.Verdict == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", c.Verdict.Confidence)
}

func noteOf(c *cell) string {
	var notes []string
	if !c.Reasoned() {
		notes = append(notes, "baseline placeholder")
	}
	if c.Disputed() {
		notes = append(notes, "critic disagrees")
	}
	if c.Flagged() && c.Verdict != nil {
		notes = append(notes, "low confidence")
	}
	if c.Verdict != nil {
		if c.Verdict.CarveoutApplied != "" {
			notes = append(notes, "carve-out")
		}
		if c.Verdict.MetricUndefined {
			notes = append(notes, "metric undefined")
		}
	}
	return strings.Join(notes, ", ")
}

func sourceLabel(c *cell) string {
	ref := c.Spec.SourceRef
	switch {
	case ref.DocID == "" || ref.DocID == c.ScenarioID:
		return "clause text"
	case ref.Page > 0:
		return fmt.Sprintf("clause text — %s p. %d", ref.DocID, ref.Page)
	default:
		return "clause text — " + ref.DocID
	}
}

func periodOf(p domain.Period) string {
	s := p.Kind
	if !p.From.IsZero() && !p.To.IsZero() {
		s += fmt.Sprintf("  %s .. %s", p.From.Format("2006-01-02"), p.To.Format("2006-01-02"))
	}
	if p.Label != "" {
		s += "  (" + p.Label + ")"
	}
	return strings.TrimSpace(s)
}

func unitSuffix(unit string) string {
	if unit == "" {
		return ""
	}
	return "  " + unit
}

func termLines(s *domain.CovenantSpec) string {
	var lines []string
	for _, t := range s.Terms {
		if t.Kind == domain.TermConstant {
			lines = append(lines, fmt.Sprintf("%s = %s", t.Name, t.Constant.String()))
			continue
		}
		line := fmt.Sprintf("%s = %s", t.Name, t.Kind)
		if t.Line != "" {
			line += fmt.Sprintf(" %q", t.Line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func documentSummary(docs []docRef) string {
	byType := make(map[string]int)
	for _, d := range docs {
		byType[string(d.DocType)]++
	}
	var parts []string
	for _, t := range domain.SortedKeys(byType) {
		parts = append(parts, fmt.Sprintf("%d %s", byType[t], t))
	}
	return fmt.Sprintf("%d effective: %s", len(docs), strings.Join(parts, ", "))
}
