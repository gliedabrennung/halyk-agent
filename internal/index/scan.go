package index

import (
	"regexp"
	"slices"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

var (
	clauseKeywordRe = regexp.MustCompile(`(?i)(?:пункт|статья|clause|section|article|п\.)\s*(\d{1,2}\.\d{1,2})\b`)

	clauseHeadingRe = regexp.MustCompile(`(?m)^[^\S\n]*(\d{1,2}\.\d{1,2})[.)]?[^\S\n]+\p{Lu}`)
	periodRe        = regexp.MustCompile(`(?:с|from)\s+(\d{4}-\d{2}-\d{2})\s+(?:по|to|until)\s+(\d{4}-\d{2}-\d{2})`)

	covenantPeriodRe = regexp.MustCompile(`(?i)(?:ковенантн\S*\s+период\S*|covenant\s+period)[^\n]{0,40}?(\d{4}-\d{2}-\d{2})\s*(?:по|to|until|[-–—])\s*(\d{4}-\d{2}-\d{2})`)
	isoDateRe        = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	yearRe           = regexp.MustCompile(`\b(20\d{2})\b`)
	currencyRe       = regexp.MustCompile(`\b(USD|EUR|KZT|GBP|CHF|CNY|RUB)\b`)
)

var _supersededMarkers = []string{
	"НЕДЕЙСТВУЮЩАЯ РЕДАКЦИЯ",
	"НЕ ПРИМЕНЯЕТСЯ",
	"ЗАМЕНЕНА",
	"ЗАМЕНЁН",
	"УТРАТИЛ СИЛУ",
	"УТРАТИЛА СИЛУ",
	"ПРЕДЫДУЩАЯ РЕДАКЦИЯ",
	"SUPERSEDED",
	"NO LONGER IN EFFECT",
}

type Scan struct {
	AccountIDs      []string `json:"account_ids"`
	ClauseNumbers   []string `json:"clause_numbers"`
	Currencies      []string `json:"currencies"`
	Superseded      bool     `json:"superseded"`
	SupersededQuote string   `json:"superseded_quote,omitempty"`
	PeriodFrom      string   `json:"period_from,omitempty"`
	PeriodTo        string   `json:"period_to,omitempty"`

	PeriodIsCovenant bool     `json:"period_is_covenant,omitempty"`
	Years            []string `json:"years,omitempty"`
	Chars            int      `json:"chars"`
}

func ScanText(text string, accounts []string) Scan {
	const bannerZone = 1500
	s := Scan{Chars: len(text)}

	var found []string
	for _, id := range accounts {
		if strings.Contains(text, id) {
			found = append(found, id)
		}
	}
	s.AccountIDs = uniqueSorted(found)
	s.Currencies = uniqueSorted(currencyRe.FindAllString(text, -1))

	for _, re := range []*regexp.Regexp{clauseKeywordRe, clauseHeadingRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			s.ClauseNumbers = append(s.ClauseNumbers, m[1])
		}
	}
	s.ClauseNumbers = uniqueSorted(s.ClauseNumbers)

	head := text
	if len(head) > bannerZone {
		head = head[:bannerZone]
	}
	upper := strings.ToUpper(head)
	for _, marker := range _supersededMarkers {
		if i := strings.Index(upper, marker); i >= 0 {
			s.Superseded = true
			s.SupersededQuote = quoteAround(text, i, 120)
			break
		}
	}

	if m := covenantPeriodRe.FindStringSubmatch(text); m != nil {
		s.PeriodFrom, s.PeriodTo = m[1], m[2]
		s.PeriodIsCovenant = true
	} else if m := periodRe.FindStringSubmatch(text); m != nil {
		s.PeriodFrom, s.PeriodTo = m[1], m[2]
	}

	years := make(map[string]bool)
	for _, m := range isoDateRe.FindAllStringSubmatch(text, -1) {
		years[m[1]] = true
	}
	for _, m := range yearRe.FindAllStringSubmatch(text, -1) {
		years[m[1]] = true
	}
	s.Years = domain.SortedKeys(years)
	return s
}

func (s Scan) HasClauses(clauses []string) bool {
	if len(clauses) == 0 {
		return false
	}
	for _, c := range clauses {
		if !slices.Contains(s.ClauseNumbers, c) {
			return false
		}
	}
	return true
}

func (s Scan) CoversYear(year string) bool {
	if s.PeriodFrom == "" {
		return false
	}
	return strings.HasPrefix(s.PeriodFrom, year) || strings.HasPrefix(s.PeriodTo, year)
}

func quoteAround(text string, i, width int) string {
	start := max(0, i-width/3)
	end := min(len(text), i+width)
	return strings.Join(strings.Fields(text[start:end]), " ")
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	set := make(map[string]bool, len(in))
	for _, v := range in {
		set[strings.ToUpper(strings.TrimSpace(v))] = true
	}
	return domain.SortedKeys(set)
}
