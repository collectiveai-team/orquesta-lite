package flow

import (
	"encoding/json"
	"fmt"
)

type Resolver func(path string) (any, bool)

func ResolveValue(value Value, resolve Resolver) (any, error) {
	if value.Ref != nil {
		resolved, ok := resolve(value.Ref.Path)
		if !ok {
			return nil, fmt.Errorf("reference %q not found", value.Ref.Path)
		}
		return resolved, nil
	}
	switch literal := value.Literal.(type) {
	case map[string]Value:
		out := make(map[string]any, len(literal))
		for key, child := range literal {
			resolved, err := ResolveValue(child, resolve)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out[key] = resolved
		}
		return out, nil
	case []Value:
		out := make([]any, len(literal))
		for index, child := range literal {
			resolved, err := ResolveValue(child, resolve)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", index, err)
			}
			out[index] = resolved
		}
		return out, nil
	default:
		return literal, nil
	}
}

// LiteralInt reports the value's literal integer when it is one. It returns
// false for a `$ref` or any non-integer literal, so callers that want the
// statically-known bound of a loop can ask without resolving anything.
func (v Value) LiteralInt() (int, bool) {
	if v.Ref != nil {
		return 0, false
	}
	number, ok := v.Literal.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	if err != nil {
		return 0, false
	}
	return int(parsed), true
}

func walkRefs(value Value, visit func(string) error) error {
	if value.Ref != nil {
		return visit(value.Ref.Path)
	}
	switch literal := value.Literal.(type) {
	case map[string]Value:
		for _, child := range literal {
			if err := walkRefs(child, visit); err != nil {
				return err
			}
		}
	case []Value:
		for _, child := range literal {
			if err := walkRefs(child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
