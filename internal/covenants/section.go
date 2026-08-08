package covenants

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	articleRe = regexp.MustCompile(`(?im)^[^\S\n]*(?:Статья|Article)\s*(\d{1,2})\s*[—–\-.:]`)

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
		return Section{}, fmt.Errorf("no article headings found")
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
		numStr := text[loc[2]:loc[3]]
		n, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		lineEnd := strings.IndexByte(text[loc[0]:], '\n')
		line := text[loc[0]:]
		if lineEnd > 0 {
			line = text[loc[0] : loc[0]+lineEnd]
		}
		out = append(out, articleOccurrence{number: n, start: loc[0], line: line})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
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
	seen := make(map[string]bool)
	for _, m := range clauseStartRe.FindAllStringSubmatch(sectionText, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

func PageOf(text string, offset int) int {
	if offset <= 0 {
		return 1
	}
	if offset > len(text) {
		offset = len(text)
	}
	return strings.Count(text[:offset], "\f") + 1
}

func CovenantArticleFor(text string, clauseIDs []string) (Section, error) {
	if len(clauseIDs) == 0 {
		return Section{}, fmt.Errorf("no clause ids requested")
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
	n := 0
	if _, err := fmt.Sscanf(major, "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

func hasAllClauses(sectionText string, clauseIDs []string) bool {
	present := make(map[string]bool, len(clauseIDs))
	for _, id := range ClauseIDs(sectionText) {
		present[id] = true
	}
	for _, id := range clauseIDs {
		if !present[id] {
			return false
		}
	}
	return true
}
