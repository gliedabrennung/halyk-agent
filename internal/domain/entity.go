package domain

import (
	"regexp"
	"strings"
)

var _legalSuffixes = map[string]bool{
	"llp": true, "llc": true, "jsc": true, "ltd": true, "lp": true,
	"ao": true, "oao": true, "zao": true, "too": true, "plc": true,
	"gmbh": true, "inc": true, "sa": true, "limited": true,
}

var _parentheticalRe = regexp.MustCompile(`\(.*?\)`)

func EntityKey(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, ".", "")
	s = _parentheticalRe.ReplaceAllString(s, " ")
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'а' && r <= 'я':
			return r
		default:
			return ' '
		}
	}, s)
	tokens := strings.Fields(s)
	for len(tokens) > 0 && _legalSuffixes[tokens[len(tokens)-1]] {
		tokens = tokens[:len(tokens)-1]
	}
	return strings.Join(tokens, " ")
}
