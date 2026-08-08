package report

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/classify"
	"github.com/gliedabrennung/halyk-agent/internal/covenants"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/engine"
	"github.com/gliedabrennung/halyk-agent/internal/facts"
	"github.com/gliedabrennung/halyk-agent/internal/index"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/gliedabrennung/halyk-agent/internal/submit"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

type dossier struct {
	Generated      time.Time
	SubmissionPath string

	Corpus    corpus
	Borrowers []borrower
	Warnings  []string
}

type corpus struct {
	Borrowers    int
	Cells        int
	Compliant    int
	Breach       int
	WithEvidence int
	Reasoned     int
	Flagged      int
	Disputed     int

	Txns      int
	Documents int
	Pages     int
}

type borrower struct {
	ScenarioID string
	Company    string
	Accounts   []string
	Txns       int

	Related          []string
	RelatedThreshold decimal.Decimal
	Adjustments      []domain.Adjustment
	FX               map[string]decimal.Decimal
	FactNotes        []string

	Docs   []docRef
	Totals []categoryTotal
	Cells  []cell
}

type docRef struct {
	DocID   string
	DocType domain.DocType
	Pages   int
	Period  string
	Summary string
}

type categoryTotal struct {
	Category domain.Category
	Amount   decimal.Decimal
	Txns     int
}

type cell struct {
	ScenarioID string
	ClauseID   string
	Title      string

	Status   string
	Actual   decimal.Decimal
	Evidence string

	EvidenceTxn *domain.Txn
	Spec        *domain.CovenantSpec
	Verdict     *domain.Verdict
}

func (c *cell) Reasoned() bool {
	return c.Verdict != nil && c.Verdict.Source == engine.SourceEngine
}

func (c *cell) Confidence() float64 {
	if c.Verdict == nil {
		return 0
	}
	return c.Verdict.Confidence
}

func (c *cell) Flagged() bool {
	const lowConfidence = 0.5
	return c.Confidence() <= lowConfidence
}

func (c *cell) Disputed() bool {
	if c.Verdict == nil {
		return false
	}
	for _, line := range c.Verdict.Trace {
		if strings.HasPrefix(line, "critic disagrees") {
			return true
		}
	}
	return false
}

func (c *cell) Critic() string {
	if c.Verdict == nil {
		return ""
	}
	for _, line := range c.Verdict.Trace {
		if strings.HasPrefix(line, "critic") {
			return line
		}
	}
	return ""
}

func (c *cell) Trace() []string {
	if c.Verdict == nil {
		return nil
	}
	var out []string
	for _, line := range c.Verdict.Trace {
		if strings.HasPrefix(line, "critic") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func collect(opts Options) (*dossier, error) {
	cfg := opts.Cfg

	tpl, txns, err := ingest.LoadTemplateAndTxns(cfg, opts.Store)
	if err != nil {
		return nil, err
	}
	led := domain.NewLedger(txns)

	scenarios, err := tpl.ScenariosFor(opts.Only)
	if err != nil {
		return nil, err
	}

	path := cfg.SubmissionPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read submission: %w (run `halyk-agent submit` first)", err)
	}
	if !gjson.ValidBytes(raw) {
		return nil, fmt.Errorf("%s is not valid JSON; re-run `halyk-agent submit`", path)
	}
	sub := gjson.ParseBytes(raw)

	verdicts, err := opts.Store.LoadVerdicts()
	if err != nil {
		return nil, err
	}
	docs, err := opts.Store.LoadDocuments()
	if err != nil {
		return nil, err
	}

	d := &dossier{
		Generated:      time.Now().UTC(),
		SubmissionPath: path,
	}
	d.Corpus.Txns = len(txns)
	d.Corpus.Documents = len(docs)
	for _, doc := range docs {
		d.Corpus.Pages += doc.Pages
	}

	var idx *index.Index
	if i, err := index.Load(opts.Store); err == nil {
		idx = i
	} else {
		d.Warnings = append(d.Warnings, "no document index: the report lists no source documents (run `halyk-agent triage`)")
	}

	for _, scn := range scenarios {
		b, warnings := collectBorrower(opts, scn, tpl, led, sub, verdicts, idx)
		d.Warnings = append(d.Warnings, warnings...)
		d.Borrowers = append(d.Borrowers, *b)

		d.Corpus.Borrowers++
		for i := range b.Cells {
			c := &b.Cells[i]
			d.Corpus.Cells++
			switch c.Status {
			case domain.StatusCompliant:
				d.Corpus.Compliant++
			case domain.StatusBreach:
				d.Corpus.Breach++
			}
			if c.Evidence != "" {
				d.Corpus.WithEvidence++
			}
			if c.Reasoned() {
				d.Corpus.Reasoned++
			}
			if c.Flagged() {
				d.Corpus.Flagged++
			}
			if c.Disputed() {
				d.Corpus.Disputed++
			}
		}
	}
	return d, nil
}

func collectBorrower(
	opts Options,
	scn string,
	tpl *domain.Template,
	led *domain.Ledger,
	sub gjson.Result,
	verdicts map[string]domain.Verdict,
	idx *index.Index,
) (*borrower, []string) {
	var warnings []string
	b := &borrower{ScenarioID: scn, Accounts: led.ScnToAccount[scn], Txns: len(led.ByScenario[scn])}

	var fb domain.FactBase
	if ok, err := opts.Store.GetArtifact(facts.ArtifactKind, scn, &fb); err == nil && ok {
		b.Company = fb.Company
		b.Related = fb.RelatedNames()
		b.RelatedThreshold = fb.RelatedPartyThreshold
		b.Adjustments = fb.Adjustments
		b.FX = fb.FXRates
		b.FactNotes = fb.Notes
	} else if err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: fact base unreadable: %v", scn, err))
	} else {
		warnings = append(warnings, scn+": no fact base (run `halyk-agent facts`)")
	}

	var labels domain.LabelSet
	if ok, err := opts.Store.GetArtifact(classify.ArtifactKind, scn, &labels); err == nil && ok {
		if b.Company == "" {
			b.Company = labels.Company
		}
		b.Totals = categoryTotals(&labels)
	} else if err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: labels unreadable: %v", scn, err))
	} else {
		warnings = append(warnings, scn+": no row labels (run `halyk-agent classify`)")
	}

	if idx != nil {
		for _, e := range idx.DocsFor(scn) {
			b.Docs = append(b.Docs, docRef{
				DocID: e.DocID, DocType: e.DocType, Pages: e.Pages,
				Period: e.Meta.Period, Summary: e.Meta.Summary,
			})
		}
	}

	specs := map[string]*domain.CovenantSpec{}
	var list []*domain.CovenantSpec
	if ok, err := opts.Store.GetArtifact(covenants.ArtifactKind, scn, &list); err == nil && ok {
		for _, s := range list {
			specs[s.ClauseID] = s
		}
	} else if err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: covenant specifications unreadable: %v", scn, err))
	} else {
		warnings = append(warnings, scn+": no covenant specifications (run `halyk-agent covenants`)")
	}

	for _, clause := range tpl.ClausesFor(scn) {
		answer := sub.Get("answers." + submit.EscapeKey(scn) + "." + submit.EscapeKey(clause))
		if !answer.Exists() {
			warnings = append(warnings, fmt.Sprintf("%s/%s: the submission has no such cell", scn, clause))
			continue
		}
		c := cell{ScenarioID: scn, ClauseID: clause, Status: answer.Get("status").String()}

		if actual := answer.Get("actual"); actual.Type == gjson.Number {
			if v, err := decimal.NewFromString(actual.Raw); err == nil {
				c.Actual = v
			} else {
				warnings = append(warnings, fmt.Sprintf("%s/%s: actual %q is not a decimal", scn, clause, actual.Raw))
			}
		} else {
			warnings = append(warnings, fmt.Sprintf("%s/%s: actual is not a number", scn, clause))
		}

		if ev := answer.Get("evidence_txn_id"); ev.Type == gjson.String {
			c.Evidence = ev.String()
			c.EvidenceTxn = led.ByID[c.Evidence]
		}

		if spec, ok := specs[clause]; ok {
			c.Spec = spec
			c.Title = spec.Title
		}
		if v, ok := verdicts[store.VerdictKey(scn, clause)]; ok {
			c.Verdict = &v
		}
		if !c.Reasoned() {
			warnings = append(warnings, fmt.Sprintf(
				"%s/%s: answered by the baseline placeholder, not by the engine", scn, clause))
		}
		b.Cells = append(b.Cells, c)
	}
	return b, warnings
}

func categoryTotals(labels *domain.LabelSet) []categoryTotal {
	counts := make(map[domain.Category]int)
	for _, t := range labels.Txns {
		counts[t.Category]++
	}
	out := make([]categoryTotal, 0, len(labels.Totals))
	for cat, amount := range labels.Totals {
		out = append(out, categoryTotal{Category: cat, Amount: amount, Txns: counts[cat]})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Amount.Abs().GreaterThan(out[j].Amount.Abs())
	})
	return out
}
