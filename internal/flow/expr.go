package flow

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type exprToken struct {
	kind  string
	text  string
	value any
}

// EvalBool evaluates the deliberately small, side-effect-free condition
// language used by `if`. Identifiers are runtime reference paths.
func EvalBool(expression string, resolve Resolver) (bool, error) {
	tokens, err := lexExpr(expression)
	if err != nil {
		return false, err
	}
	parser := exprParser{tokens: tokens, resolve: resolve}
	value, err := parser.parseOr()
	if err != nil {
		return false, err
	}
	if parser.peek() != nil {
		return false, fmt.Errorf("expression: unexpected token %q", parser.peek().text)
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("expression did not produce boolean")
	}
	return result, nil
}

// ExprReferences returns every runtime reference path an expression reads, in
// order of appearance. An expression that does not lex yields no paths: the
// compiler reports that as its own diagnostic, and a validator built on this
// must not invent references out of a broken string.
//
// `if` and `while.condition` are raw expression strings, not Values, so
// IR.ReferencePaths() structurally cannot see through them by walking the
// value tree. Without this, a whole class of reference — `config.<key>` in a
// gate's guard being the one that bit — compiled, passed `flow validate`,
// passed the pre-run namespace check, created the run, and only then died on
// an unresolvable path partway through the flow.
func ExprReferences(expression string) []string {
	tokens, err := lexExpr(expression)
	if err != nil {
		return nil
	}
	var paths []string
	for _, token := range tokens {
		if token.kind == "ref" {
			paths = append(paths, token.text)
		}
	}
	return paths
}

func lexExpr(input string) ([]exprToken, error) {
	var tokens []exprToken
	for index := 0; index < len(input); {
		char := input[index]
		if unicode.IsSpace(rune(char)) {
			index++
			continue
		}
		matched := false
		for _, op := range []string{"&&", "||", "==", "!=", ">=", "<="} {
			if strings.HasPrefix(input[index:], op) {
				tokens = append(tokens, exprToken{kind: "op", text: op})
				index += len(op)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		switch char {
		case '!', '>', '<':
			tokens = append(tokens, exprToken{kind: "op", text: string(char)})
			index++
		case '(':
			tokens = append(tokens, exprToken{kind: "left", text: "("})
			index++
		case ')':
			tokens = append(tokens, exprToken{kind: "right", text: ")"})
			index++
		case '"':
			start := index
			index++
			for index < len(input) && input[index] != '"' {
				if input[index] == '\\' {
					index++
				}
				index++
			}
			if index >= len(input) {
				return nil, fmt.Errorf("expression: unterminated string")
			}
			index++
			value, err := strconv.Unquote(input[start:index])
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, exprToken{kind: "literal", text: input[start:index], value: value})
		default:
			if unicode.IsDigit(rune(char)) || char == '-' {
				start := index
				index++
				for index < len(input) && (unicode.IsDigit(rune(input[index])) || input[index] == '.') {
					index++
				}
				number, err := strconv.ParseFloat(input[start:index], 64)
				if err != nil {
					return nil, fmt.Errorf("expression: %w", err)
				}
				tokens = append(tokens, exprToken{kind: "literal", text: input[start:index], value: number})
				continue
			}
			if isExprIdent(char) {
				start := index
				index++
				for index < len(input) && (isExprIdent(input[index]) || unicode.IsDigit(rune(input[index])) || input[index] == '.' || input[index] == '-') {
					index++
				}
				word := input[start:index]
				switch word {
				case "true":
					tokens = append(tokens, exprToken{kind: "literal", text: word, value: true})
				case "false":
					tokens = append(tokens, exprToken{kind: "literal", text: word, value: false})
				case "null":
					tokens = append(tokens, exprToken{kind: "literal", text: word, value: nil})
				default:
					tokens = append(tokens, exprToken{kind: "ref", text: word})
				}
				continue
			}
			return nil, fmt.Errorf("expression: unexpected character %q", char)
		}
	}
	return tokens, nil
}

func isExprIdent(char byte) bool { return char == '_' || unicode.IsLetter(rune(char)) }

type exprParser struct {
	tokens   []exprToken
	position int
	resolve  Resolver
}

func (p *exprParser) peek() *exprToken {
	if p.position >= len(p.tokens) {
		return nil
	}
	return &p.tokens[p.position]
}
func (p *exprParser) take() *exprToken {
	token := p.peek()
	if token != nil {
		p.position++
	}
	return token
}

func (p *exprParser) parseOr() (any, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek() != nil && p.peek().text == "||" {
		p.take()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		lb, lok := left.(bool)
		rb, rok := right.(bool)
		if !lok || !rok {
			return nil, fmt.Errorf("expression: || requires booleans")
		}
		left = lb || rb
	}
	return left, nil
}
func (p *exprParser) parseAnd() (any, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek() != nil && p.peek().text == "&&" {
		p.take()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		lb, lok := left.(bool)
		rb, rok := right.(bool)
		if !lok || !rok {
			return nil, fmt.Errorf("expression: && requires booleans")
		}
		left = lb && rb
	}
	return left, nil
}
func (p *exprParser) parseNot() (any, error) {
	if p.peek() != nil && p.peek().text == "!" {
		p.take()
		value, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		boolean, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("expression: ! requires boolean")
		}
		return !boolean, nil
	}
	return p.parseCompare()
}
func (p *exprParser) parseCompare() (any, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	if p.peek() == nil {
		return left, nil
	}
	op := p.peek().text
	if op != "==" && op != "!=" && op != ">" && op != ">=" && op != "<" && op != "<=" {
		return left, nil
	}
	p.take()
	right, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	if op == "==" || op == "!=" {
		equal := valuesEqual(left, right)
		if op == "!=" {
			equal = !equal
		}
		return equal, nil
	}
	l, lok := numeric(left)
	r, rok := numeric(right)
	if !lok || !rok {
		leftString, leftOK := left.(string)
		rightString, rightOK := right.(string)
		if !leftOK || !rightOK {
			return nil, fmt.Errorf("expression: %s requires two numbers or two strings", op)
		}
		switch op {
		case ">":
			return leftString > rightString, nil
		case ">=":
			return leftString >= rightString, nil
		case "<":
			return leftString < rightString, nil
		default:
			return leftString <= rightString, nil
		}
	}
	switch op {
	case ">":
		return l > r, nil
	case ">=":
		return l >= r, nil
	case "<":
		return l < r, nil
	default:
		return l <= r, nil
	}
}
func (p *exprParser) parsePrimary() (any, error) {
	token := p.take()
	if token == nil {
		return nil, fmt.Errorf("expression: unexpected end")
	}
	if token.kind == "left" {
		value, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.take() == nil || p.tokens[p.position-1].kind != "right" {
			return nil, fmt.Errorf("expression: missing )")
		}
		return value, nil
	}
	if token.kind == "literal" {
		return token.value, nil
	}
	if token.kind == "ref" {
		value, ok := p.resolve(token.text)
		if !ok {
			return nil, fmt.Errorf("expression: reference %q not found", token.text)
		}
		return value, nil
	}
	return nil, fmt.Errorf("expression: unexpected token %q", token.text)
}
func numeric(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	}
	return 0, false
}
