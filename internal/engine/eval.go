package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// evalExpr interpolates the expression against the context, then evaluates the
// resulting constant boolean expression. Supported: true, false, numbers,
// double-quoted strings, ==, !=, &&, ||, !, and parentheses. Bare identifiers
// not produced by interpolation are resolved against the context as a fallback.
func (e *Engine) evalExpr(c *Context, expr string) (bool, error) {
	interpolated := c.Interpolate(expr)
	toks, err := tokenize(interpolated)
	if err != nil {
		return false, fmt.Errorf("engine: tokenize %q: %w", interpolated, err)
	}
	p := &parser{toks: toks}
	v, err := p.parseExpr(c)
	if err != nil {
		return false, fmt.Errorf("engine: eval %q: %w", interpolated, err)
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("engine: expression %q did not yield a bool (%v)", interpolated, v)
	}
	return b, nil
}

// runEval evaluates an expression and stores the boolean result.
func (e *Engine) runEval(_ context.Context, c *Context, s Step) error {
	v, err := e.evalExpr(c, s.Expression)
	if err != nil {
		return err
	}
	if len(s.Outputs) == 0 {
		return nil
	}
	applyOutputs(c, s.Outputs, v)
	return nil
}

type token struct {
	kind string // "bool","num","str","ident","op","lparen","rparen"
	text string
	num  float64
	b    bool
}

func tokenize(s string) ([]token, error) {
	var out []token
	i := 0
	for i < len(s) {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case ch == '(':
			out = append(out, token{kind: "lparen"})
			i++
		case ch == ')':
			out = append(out, token{kind: "rparen"})
			i++
		case strings.HasPrefix(s[i:], "==") || strings.HasPrefix(s[i:], "!="):
			out = append(out, token{kind: "op", text: s[i : i+2]})
			i += 2
		case ch == '&' && i+1 < len(s) && s[i+1] == '&':
			out = append(out, token{kind: "op", text: "&&"})
			i += 2
		case ch == '|' && i+1 < len(s) && s[i+1] == '|':
			out = append(out, token{kind: "op", text: "||"})
			i += 2
		case ch == '!':
			out = append(out, token{kind: "op", text: "!"})
			i++
		case ch == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string")
			}
			out = append(out, token{kind: "str", text: s[i+1 : j]})
			i = j + 1
		case unicode.IsDigit(rune(ch)):
			j := i
			for j < len(s) && (unicode.IsDigit(rune(s[j])) || s[j] == '.') {
				j++
			}
			n, err := strconv.ParseFloat(s[i:j], 64)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: "num", num: n})
			i = j
		case isIdentStart(ch):
			j := i
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}
			word := s[i:j]
			i = j
			switch word {
			case "true":
				out = append(out, token{kind: "bool", b: true})
			case "false":
				out = append(out, token{kind: "bool", b: false})
			default:
				out = append(out, token{kind: "ident", text: word})
			}
		default:
			return nil, fmt.Errorf("unexpected char %q", ch)
		}
	}
	return out, nil
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
func isIdentPart(b byte) bool {
	return isIdentStart(b) || unicode.IsDigit(rune(b)) || b == '.'
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() *token {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}
func (p *parser) next() *token {
	t := p.peek()
	if t != nil {
		p.pos++
	}
	return t
}

// parseExpr := orExpr
func (p *parser) parseExpr(c *Context) (any, error) {
	return p.parseOr(c)
}

func (p *parser) parseOr(c *Context) (any, error) {
	left, err := p.parseAnd(c)
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != "op" || t.text != "||" {
			return left, nil
		}
		p.next()
		right, err := p.parseAnd(c)
		if err != nil {
			return nil, err
		}
		left = toBool(left) || toBool(right)
	}
}

func (p *parser) parseAnd(c *Context) (any, error) {
	left, err := p.parseNot(c)
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != "op" || t.text != "&&" {
			return left, nil
		}
		p.next()
		right, err := p.parseNot(c)
		if err != nil {
			return nil, err
		}
		left = toBool(left) && toBool(right)
	}
}

func (p *parser) parseNot(c *Context) (any, error) {
	t := p.peek()
	if t != nil && t.kind == "op" && t.text == "!" {
		p.next()
		v, err := p.parseNot(c)
		if err != nil {
			return nil, err
		}
		return !toBool(v), nil
	}
	return p.parseCmp(c)
}

func (p *parser) parseCmp(c *Context) (any, error) {
	left, err := p.parsePrimary(c)
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t == nil || t.kind != "op" || (t.text != "==" && t.text != "!=") {
		return left, nil
	}
	op := t.text
	p.next()
	right, err := p.parsePrimary(c)
	if err != nil {
		return nil, err
	}
	eq := equal(left, right)
	if op == "!=" {
		return !eq, nil
	}
	return eq, nil
}

func (p *parser) parsePrimary(c *Context) (any, error) {
	t := p.next()
	if t == nil {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	switch t.kind {
	case "lparen":
		v, err := p.parseExpr(c)
		if err != nil {
			return nil, err
		}
		if r := p.next(); r == nil || r.kind != "rparen" {
			return nil, fmt.Errorf("missing )")
		}
		return v, nil
	case "bool":
		return t.b, nil
	case "num":
		return t.num, nil
	case "str":
		return t.text, nil
	case "ident":
		v, ok := c.Get(t.text)
		if !ok {
			return nil, fmt.Errorf("unknown identifier %q", t.text)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unexpected token %+v", t)
	}
}

func toBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != "" && x != "false" && x != "0"
	default:
		return x != nil
	}
}

func equal(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
