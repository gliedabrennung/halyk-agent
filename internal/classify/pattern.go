package classify

import (
	"regexp"
	"strings"
)

var _separatorRe = regexp.MustCompile(`\s+[—–-]\s+`)

var _tailRe = regexp.MustCompile(`(?i)\s+(?:` +
	`(?:first|second|third|fourth)\s+quarter|` +
	`q[1-4](?:\s+20\d\d)?|` +
	`20\d\d` +
	`)$`)

func Pattern(description string) string {
	s := strings.ToLower(strings.TrimSpace(description))
	if parts := _separatorRe.Split(s, 2); len(parts) > 0 {
		s = parts[0]
	}
	for {
		trimmed := _tailRe.ReplaceAllString(strings.TrimSpace(s), "")
		if trimmed == s {
			break
		}
		s = trimmed
	}
	return strings.Join(strings.Fields(s), " ")
}
