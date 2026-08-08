package covenants

import (
	"errors"
	"strings"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/index"
)

const _agreementText = `Статья 6 — Финансовые ковенанты

Пункт 6.1 Заёмщик обеспечивает, чтобы Капитальные затраты не превышали $2,000,000.00.

Пункт 6.2 Заёмщик обеспечивает, чтобы Выручка составляла не менее $3,500,000.00.
`

func agreementIndex(scenarioID, docID string) *index.Index {
	return &index.Index{Entries: []index.Entry{{
		DocID:      docID,
		DocType:    domain.DocCreditAgreement,
		ScenarioID: scenarioID,
		Effective:  true,
		Meta:       agents.TriageResult{CompanyName: "Aktau Port Services JSC"},
	}}}
}

func textOf(docID, text string) func(string) (string, error) {
	return func(id string) (string, error) {
		if id != docID {
			return "", errors.New("no such document")
		}
		return text, nil
	}
}

func TestClauseJobsBuildsOnePerClause(t *testing.T) {
	jobs, loc, err := clauseJobs(textOf("doc1", _agreementText), agreementIndex("P1", "doc1"), "P1", []string{"6.1", "6.2"})
	if err != nil {
		t.Fatalf("clauseJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("built %d jobs, want 2", len(jobs))
	}
	if loc.DocID != "doc1" || loc.Article != 6 {
		t.Errorf("located = %+v, want the article 6 of doc1", loc)
	}
	if !strings.Contains(jobs[0].input.ClauseText, "2,000,000.00") {
		t.Errorf("6.1 carries the wrong clause text: %q", jobs[0].input.ClauseText)
	}
	if jobs[1].clause != "6.2" || jobs[1].input.Company != "Aktau Port Services JSC" {
		t.Errorf("second job = %+v", jobs[1])
	}
}

func TestClauseJobsReportsWhatItCannotBuild(t *testing.T) {
	tests := []struct {
		name    string
		idx     *index.Index
		docText func(string) (string, error)
		clauses []string
		want    string
	}{
		{
			name:    "no agreement for this borrower",
			idx:     agreementIndex("P2", "doc1"),
			docText: textOf("doc1", _agreementText),
			clauses: []string{"6.1"},
			want:    "no effective credit agreement",
		},
		{
			name:    "the agreement text is unreadable",
			idx:     agreementIndex("P1", "doc1"),
			docText: textOf("other", _agreementText),
			clauses: []string{"6.1"},
			want:    "load agreement doc1",
		},
		{
			name:    "the article holds no such clause",
			idx:     agreementIndex("P1", "doc1"),
			docText: textOf("doc1", _agreementText),
			clauses: []string{"6.9"},
			want:    "no single article contains",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs, _, err := clauseJobs(tt.docText, tt.idx, "P1", tt.clauses)
			if err == nil {
				t.Fatalf("want an error, got %d jobs", len(jobs))
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
			if jobs != nil {
				t.Errorf("a failed borrower must contribute no jobs, got %d", len(jobs))
			}
		})
	}
}
