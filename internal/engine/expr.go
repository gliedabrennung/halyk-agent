package engine

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"
)

func EvalExpr(expr string, vars map[string]decimal.Decimal) (decimal.Decimal, error) {
	toks, err := tokenize(expr)
	if err != nil {
		return decimal.Zero, err
	}
	p := &parser{toks: toks, vars: vars}
	v, err := p.parseExpr()
	if err != nil {
		return decimal.Zero, err
	}
	if p.pos != len(p.toks) {
		return decimal.Zero, fmt.Errorf("unexpected %q at position %d in %q", p.toks[p.pos].text, p.pos, expr)
	}
	return v, nil
}

func ExprIdentifiers(expr string) ([]string, error) {
	toks, err := tokenize(expr)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(toks))
	var out []string
	for _, t := range toks {
		if t.kind != tokIdent || isFunc(t.text) {
			continue
		}
		if !seen[t.text] {
			seen[t.text] = true
			out = append(out, t.text)
		}
	}
	return out, nil
}

type tokKind int

const (
	tokNumber tokKind = iota + 1
	tokIdent
	tokOp
	tokLParen
	tokRParen
	tokComma
)

type token struct {
	kind tokKind
	text string
	num  decimal.Decimal
}

func isFunc(s string) bool { return s == "max" || s == "min" }

func tokenize(s string) ([]token, error) {
	var out []token
	rs := []rune(s)
	// Длина пробега рун от i, пока они удовлетворяют ok.
	run := func(i int, ok func(rune) bool) int {
		j := i
		for j < len(rs) && ok(rs[j]) {
			j++
		}
		return j
	}
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '(':
			out = append(out, token{kind: tokLParen, text: "("})
			i++
		case r == ')':
			out = append(out, token{kind: tokRParen, text: ")"})
			i++
		case r == ',':
			out = append(out, token{kind: tokComma, text: ","})
			i++
		case strings.ContainsRune("+-*/", r):
			out = append(out, token{kind: tokOp, text: string(r)})
			i++
		case unicode.IsDigit(r) || r == '.':
			j := run(i, func(r rune) bool { return unicode.IsDigit(r) || r == '.' })
			d, err := decimal.NewFromString(string(rs[i:j]))
			if err != nil {
				return nil, fmt.Errorf("bad number %q", string(rs[i:j]))
			}
			out = append(out, token{kind: tokNumber, text: string(rs[i:j]), num: d})
			i = j
		case unicode.IsLetter(r) || r == '_':
			j := run(i, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' })
			out = append(out, token{kind: tokIdent, text: strings.ToLower(string(rs[i:j]))})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q in expression", string(r))
		}
	}
	return out, nil
}

type parser struct {
	toks []token
	pos  int
	vars map[string]decimal.Decimal
}

func (p *parser) peek() (token, bool) {
	if p.pos >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.pos], true
}

func (p *parser) parseExpr() (decimal.Decimal, error) {
	left, err := p.parseTerm()
	if err != nil {
		return decimal.Zero, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tokOp || (t.text != "+" && t.text != "-") {
			return left, nil
		}
		p.pos++
		right, err := p.parseTerm()
		if err != nil {
			return decimal.Zero, err
		}
		if t.text == "+" {
			left = left.Add(right)
		} else {
			left = left.Sub(right)
		}
	}
}

const _divisionScale = 16

func (p *parser) parseTerm() (decimal.Decimal, error) {
	left, err := p.parseFactor()
	if err != nil {
		return decimal.Zero, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tokOp || (t.text != "*" && t.text != "/") {
			return left, nil
		}
		p.pos++
		right, err := p.parseFactor()
		if err != nil {
			return decimal.Zero, err
		}
		if t.text == "*" {
			left = left.Mul(right)
			continue
		}
		if right.IsZero() {

			return decimal.Zero, errDivByZero
		}
		left = left.DivRound(right, _divisionScale)
	}
}

var errDivByZero = errors.New("division by zero")

func IsDivByZero(err error) bool { return errors.Is(err, errDivByZero) }

func (p *parser) parseFactor() (decimal.Decimal, error) {
	t, ok := p.peek()
	if !ok {
		return decimal.Zero, fmt.Errorf("expression ends where a value was expected")
	}
	if t.kind == tokOp {
		switch t.text {
		case "-":
			p.pos++
			v, err := p.parseFactor()
			if err != nil {
				return decimal.Zero, err
			}
			return v.Neg(), nil
		case "+":
			p.pos++
			return p.parseFactor()
		}
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (decimal.Decimal, error) {
	t, ok := p.peek()
	if !ok {
		return decimal.Zero, fmt.Errorf("expression ends where a value was expected")
	}
	switch t.kind {
	case tokNumber:
		p.pos++
		return t.num, nil

	case tokIdent:
		p.pos++
		if isFunc(t.text) {
			return p.parseCall(t.text)
		}
		v, found := p.vars[t.text]
		if !found {
			return decimal.Zero, fmt.Errorf("term %q has no value", t.text)
		}
		return v, nil

	case tokLParen:
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return decimal.Zero, err
		}
		if nt, ok := p.peek(); !ok || nt.kind != tokRParen {
			return decimal.Zero, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return v, nil

	case tokOp, tokRParen, tokComma:
		return decimal.Zero, fmt.Errorf("unexpected %q", t.text)

	default:
		return decimal.Zero, fmt.Errorf("unexpected token %q of kind %d", t.text, t.kind)
	}
}

func (p *parser) parseCall(name string) (decimal.Decimal, error) {
	if t, ok := p.peek(); !ok || t.kind != tokLParen {
		return decimal.Zero, fmt.Errorf("%s must be followed by (", name)
	}
	p.pos++

	var args []decimal.Decimal
	for {
		v, err := p.parseExpr()
		if err != nil {
			return decimal.Zero, err
		}
		args = append(args, v)
		t, ok := p.peek()
		if !ok {
			return decimal.Zero, fmt.Errorf("%s( is never closed", name)
		}
		if t.kind == tokComma {
			p.pos++
			continue
		}
		if t.kind == tokRParen {
			p.pos++
			break
		}
		return decimal.Zero, fmt.Errorf("unexpected %q inside %s()", t.text, name)
	}
	if len(args) < 2 {
		return decimal.Zero, fmt.Errorf("%s() needs at least two arguments", name)
	}
	if name == "max" {
		return slices.MaxFunc(args, decimal.Decimal.Cmp), nil
	}
	return slices.MinFunc(args, decimal.Decimal.Cmp), nil
}

var _conditionOps = []string{">=", "<=", "==", "!=", ">", "<"}

func EvalCondition(cond string, vars map[string]decimal.Decimal) (bool, error) {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return false, fmt.Errorf("empty condition")
	}
	for _, op := range _conditionOps {
		i := strings.Index(cond, op)
		if i <= 0 {
			continue
		}
		left, err := EvalExpr(cond[:i], vars)
		if err != nil {
			return false, err
		}
		right, err := EvalExpr(cond[i+len(op):], vars)
		if err != nil {
			return false, err
		}
		return Compare(left, op, right), nil
	}
	return false, fmt.Errorf("condition %q has no comparison operator", cond)
}

func Compare(left decimal.Decimal, op string, right decimal.Decimal) bool {
	switch op {
	case ">":
		return left.GreaterThan(right)
	case ">=":
		return left.GreaterThanOrEqual(right)
	case "<":
		return left.LessThan(right)
	case "<=":
		return left.LessThanOrEqual(right)
	case "==":
		return left.Equal(right)
	case "!=":
		return !left.Equal(right)
	}
	return false
}
