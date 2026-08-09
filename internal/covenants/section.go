package covenants

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	articleRe = regexp.MustCompile(`(?im)^[^\S\n]*(?:Статья|Article)\s+(\d{1,2}|[IVXLCivxlc]{1,7})\b`)

	clauseStartRe = regexp.MustCompile(`(?im)(?:^|\s)(?:Пункт|Clause|Section)\s*(\d{1,2}\.\d{1,2})\b`)
)

type Section struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Text   string `json:"text"`

	Start int `json:"start"`
}

type articleOccurrence struct {
	number int
	start  int
	line   string
}

func Article(text string, n int) (Section, error) {
	occs := articleOccurrences(text)
	if len(occs) == 0 {
		return Section{}, errors.New("no article headings found")
	}

	var best Section
	found := false
	for i, occ := range occs {
		if occ.number != n {
			continue
		}
		end := len(text)

		for _, next := range occs[i+1:] {
			if next.start > occ.start && next.number != n {
				end = next.start
				break
			}
		}
		body := text[occ.start:end]
		if !found || len(body) > len(best.Text) {
			best = Section{Number: n, Title: strings.TrimSpace(occ.line), Text: body, Start: occ.start}
			found = true
		}
	}
	if !found {
		return Section{}, fmt.Errorf("article %d not found", n)
	}
	return best, nil
}

func articleOccurrences(text string) []articleOccurrence {
	var out []articleOccurrence
	for _, loc := range articleRe.FindAllStringSubmatchIndex(text, -1) {
		n, ok := articleNumber(text[loc[2]:loc[3]])
		if !ok {
			continue
		}
		lineEnd := strings.IndexByte(text[loc[0]:], '\n')
		line := text[loc[0]:]
		if lineEnd > 0 {
			line = text[loc[0] : loc[0]+lineEnd]
		}
		out = append(out, articleOccurrence{number: n, start: loc[0], line: line})
	}
	slices.SortStableFunc(out, func(a, b articleOccurrence) int { return cmp.Compare(a.start, b.start) })
	return out
}

var _romanValues = map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100}

func articleNumber(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	upper := strings.ToUpper(s)
	total, prev := 0, 0
	for i := len(upper) - 1; i >= 0; i-- {
		v, ok := _romanValues[upper[i]]
		if !ok {
			return 0, false
		}
		if v < prev {
			total -= v
			continue
		}
		total, prev = total+v, v
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

func Clause(sectionText, clauseID string) (string, error) {
	locs := clauseStartRe.FindAllStringSubmatchIndex(sectionText, -1)
	for i, loc := range locs {
		if sectionText[loc[2]:loc[3]] != clauseID {
			continue
		}
		end := len(sectionText)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		return strings.TrimSpace(sectionText[loc[0]:end]), nil
	}
	return "", fmt.Errorf("clause %s not found in the section", clauseID)
}

func ClauseIDs(sectionText string) []string {
	var out []string
	for _, m := range clauseStartRe.FindAllStringSubmatch(sectionText, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

func PageOf(text string, offset int) int {
	if offset <= 0 {
		return 1
	}
	return strings.Count(text[:min(offset, len(text))], "\f") + 1
}

func CovenantArticleFor(text string, clauseIDs []string) (Section, error) {
	if len(clauseIDs) == 0 {
		return Section{}, errors.New("no clause ids requested")
	}

	if n, ok := articleNumberOf(clauseIDs[0]); ok {
		if sec, err := Article(text, n); err == nil && hasAllClauses(sec.Text, clauseIDs) {
			return sec, nil
		}
	}
	for _, occ := range articleOccurrences(text) {
		sec, err := Article(text, occ.number)
		if err != nil {
			continue
		}
		if hasAllClauses(sec.Text, clauseIDs) {
			return sec, nil
		}
	}
	return Section{}, fmt.Errorf("no single article contains all of %s", strings.Join(clauseIDs, ", "))
}

func articleNumberOf(clauseID string) (int, bool) {
	major, _, ok := strings.Cut(clauseID, ".")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0, false
	}
	return n, true
}

func hasAllClauses(sectionText string, clauseIDs []string) bool {
	present := ClauseIDs(sectionText)
	for _, id := range clauseIDs {
		if !slices.Contains(present, id) {
			return false
		}
	}
	return true
}
