package index

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/store"
)

const (
	ResolvedByAccount      = "account_id"
	ResolvedByCompany      = "company_name"
	ResolvedByCounterparty = "counterparties"
	Unresolved             = "unresolved"
)

type Entry struct {
	DocID      string              `json:"doc_id"`
	Path       string              `json:"path"`
	Pages      int                 `json:"pages"`
	DocType    domain.DocType      `json:"doc_type"`
	ScenarioID string              `json:"scenario_id,omitempty"`
	ResolvedBy string              `json:"resolved_by"`
	Effective  bool                `json:"effective"`
	Meta       agents.TriageResult `json:"meta"`
	Scan       Scan                `json:"scan"`
	Notes      []string            `json:"notes,omitempty"`
}

type Index struct {
	Entries    []Entry             `json:"entries"`
	ByScenario map[string][]string `json:"by_scenario"`
	Companies  map[string]string   `json:"companies"`

	GroupParents map[string]string `json:"group_parents,omitempty"`
}

func Resolve(entries []Entry, led *domain.Ledger, templateScenarios []string, coveredYear string) *Index {
	inTemplate := make(map[string]bool, len(templateScenarios))
	for _, s := range templateScenarios {
		inTemplate[s] = true
	}

	idx := &Index{
		ByScenario:   make(map[string][]string),
		Companies:    make(map[string]string),
		GroupParents: make(map[string]string),
	}

	for i := range entries {
		e := &entries[i]
		var scenarios []string
		for _, acc := range e.Scan.AccountIDs {
			if scn, ok := led.AccountToScn[acc]; ok && inTemplate[scn] {
				scenarios = appendUnique(scenarios, scn)
			}
		}
		switch len(scenarios) {
		case 0:
		case 1:
			e.ScenarioID, e.ResolvedBy = scenarios[0], ResolvedByAccount
			if name := normaliseCompany(e.Meta.CompanyName); name != "" {
				if prev, ok := idx.Companies[name]; ok && prev != e.ScenarioID {
					e.Notes = append(e.Notes, fmt.Sprintf(
						"company %q already maps to %s; not reusing this name for resolution", e.Meta.CompanyName, prev))
					idx.Companies[name] = ""
				} else if !ok {
					idx.Companies[name] = e.ScenarioID
				}
			}
		default:
			sort.Strings(scenarios)
			e.ResolvedBy = Unresolved
			e.Notes = append(e.Notes, "mentions several borrower accounts: "+strings.Join(scenarios, ", "))
		}
	}

	for i := range entries {
		e := &entries[i]
		if e.ScenarioID != "" {
			continue
		}
		if scn := idx.Companies[normaliseCompany(e.Meta.CompanyName)]; scn != "" {
			e.ScenarioID, e.ResolvedBy = scn, ResolvedByCompany
		}
	}

	cpToScenarios := make(map[string]map[string]bool)
	for i := range led.Txns {
		t := &led.Txns[i]
		if !inTemplate[t.ScenarioID] {
			continue
		}
		key := normaliseCompany(t.Counterparty)
		if cpToScenarios[key] == nil {
			cpToScenarios[key] = make(map[string]bool)
		}
		cpToScenarios[key][t.ScenarioID] = true
	}
	for i := range entries {
		e := &entries[i]
		if e.ScenarioID != "" {
			continue
		}
		votes := make(map[string]int)
		for _, cp := range e.Meta.Counterparties {
			owners := cpToScenarios[normaliseCompany(cp)]
			if len(owners) != 1 {
				continue
			}
			for scn := range owners {
				votes[scn]++
			}
		}
		best, second, bestScn := 0, 0, ""
		for scn, n := range votes {
			if n > best {
				best, second, bestScn = n, best, scn
			} else if n > second {
				second = n
			}
		}
		if best >= 2 && best > second {
			e.ScenarioID, e.ResolvedBy = bestScn, ResolvedByCounterparty
			e.Notes = append(e.Notes, fmt.Sprintf("matched on %d exclusive counterparties", best))
			continue
		}
		if e.ResolvedBy == "" {
			e.ResolvedBy = Unresolved
		}
	}

	for i := range entries {
		e := &entries[i]
		e.Effective = true
		switch {
		case e.Scan.Superseded:
			e.Effective = false
			e.Notes = append(e.Notes, "superseded banner: "+e.Scan.SupersededQuote)
		case e.Meta.IsSuperseded:
			e.Effective = false
			e.Notes = append(e.Notes, "the model read this as a prior revision")
		case governsByPeriod(e.DocType) && e.Scan.PeriodFrom != "" && !e.Scan.CoversYear(coveredYear):
			e.Effective = false
			e.Notes = append(e.Notes, fmt.Sprintf("covenant period %s..%s does not cover %s",
				e.Scan.PeriodFrom, e.Scan.PeriodTo, coveredYear))
		}
	}

	idx.Entries = entries
	sort.Slice(idx.Entries, func(i, j int) bool { return idx.Entries[i].DocID < idx.Entries[j].DocID })
	for _, e := range idx.Entries {
		if e.ScenarioID != "" {
			idx.ByScenario[e.ScenarioID] = append(idx.ByScenario[e.ScenarioID], e.DocID)
		}
	}
	return idx
}

func (idx *Index) LinkGroupParents(text func(docID string) (string, error)) error {
	byName := make(map[string]string, len(idx.Companies))
	for name, scn := range idx.Companies {
		if name != "" && scn != "" {
			byName[name] = scn
		}
	}
	if len(byName) == 0 {
		return nil
	}

	for i := range idx.Entries {
		e := &idx.Entries[i]
		if e.ScenarioID != "" || e.DocType != domain.DocAuditReport {
			continue
		}
		body, err := text(e.DocID)
		if err != nil {
			return err
		}
		if !mentionsConsolidation(body) {
			continue
		}
		haystack := normaliseCompany(body)

		var hits []string
		for name, scn := range byName {
			if strings.Contains(haystack, name) {
				hits = appendUnique(hits, scn)
			}
		}
		if len(hits) != 1 {
			continue
		}
		scn := hits[0]
		if prev, taken := idx.GroupParents[scn]; taken && prev != e.DocID {
			e.Notes = append(e.Notes, fmt.Sprintf("%s already has group statements in %s", scn, prev))
			continue
		}
		idx.GroupParents[scn] = e.DocID
		e.Notes = append(e.Notes, "consolidated statements of the group "+scn+" belongs to")
	}
	return nil
}

var _consolidationMarkers = []string{"consolidated", "консолидир"}

func mentionsConsolidation(body string) bool {
	lower := strings.ToLower(body)
	for _, m := range _consolidationMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func governsByPeriod(t domain.DocType) bool {
	return t == domain.DocCreditAgreement || t == domain.DocAmendment
}

func (idx *Index) CreditAgreements(scenarioID string) []Entry {
	var out []Entry
	for _, e := range idx.Entries {
		if e.ScenarioID == scenarioID && e.Effective && e.DocType == domain.DocCreditAgreement {
			out = append(out, e)
		}
	}
	return out
}

func (idx *Index) DocsFor(scenarioID string, types ...domain.DocType) []Entry {
	want := make(map[domain.DocType]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	var out []Entry
	for _, e := range idx.Entries {
		if e.ScenarioID != scenarioID || !e.Effective {
			continue
		}
		if len(want) == 0 || want[e.DocType] {
			out = append(out, e)
		}
	}
	return out
}

func (idx *Index) CheckCoverage(templateScenarios []string, clauses map[string][]string) error {
	var problems []string
	for _, scn := range templateScenarios {
		agreements := idx.CreditAgreements(scn)
		if len(agreements) == 0 {
			problems = append(problems, fmt.Sprintf(
				"%s: no effective credit agreement among %d resolved documents", scn, len(idx.ByScenario[scn])))
			continue
		}
		if want := clauses[scn]; len(want) > 0 {
			covered := false
			for _, a := range agreements {
				if a.Scan.HasClauses(want) {
					covered = true
					break
				}
			}
			if !covered {
				problems = append(problems, fmt.Sprintf(
					"%s: no effective credit agreement contains all requested clauses %s",
					scn, strings.Join(want, ", ")))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("document coverage is incomplete:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func normaliseCompany(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return ""
	}
	for _, suffix := range []string{
		" jsc", " llp", " llc", " ltd", " l.l.p.", " l.l.c.", " inc", " plc", " ао", " тоо", " оао", " зао",
	} {
		s = strings.ReplaceAll(s, suffix, " ")
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'а' && r <= 'я', r == 'ё':
			return r
		case r == ' ':
			return ' '
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func Load(st *store.Store) (*Index, error) {
	var idx Index
	ok, err := st.GetArtifact(ArtifactKind, ArtifactID, &idx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no document index; run `halyk-agent triage` first")
	}
	return &idx, nil
}
