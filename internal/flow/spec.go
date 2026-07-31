// Package flow loads and compiles versioned declarative flow definitions.
package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const APIVersionV2 = "orq.dev/v2"

type Kind string

const (
	KindFlow    Kind = "Flow"
	KindSubflow Kind = "Subflow"
)

type Metadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Policy optionally names the run policy this flow was designed for
	// (`policy:name@version`). The compiler resolves and pins it exactly like a
	// step's retry policy, and `flow run` applies it when no explicit --policy
	// is given, so a pack's declared budget no longer depends on the operator
	// remembering to name it.
	Policy string `json:"policy,omitempty"`
}

type InputSpec struct {
	Schema  string `json:"schema"`
	Default *Value `json:"default,omitempty"`
}

type Document struct {
	APIVersion string               `json:"apiVersion"`
	Kind       Kind                 `json:"kind"`
	Metadata   Metadata             `json:"metadata"`
	Inputs     map[string]InputSpec `json:"inputs,omitempty"`
	Steps      []StepSpec           `json:"steps"`
	Outputs    map[string]Value     `json:"outputs,omitempty"`
}

type ForeachSpec struct {
	Items          Value  `json:"items"`
	As             string `json:"as,omitempty"`
	MaxConcurrency int    `json:"maxConcurrency,omitempty"`
	IsolationKey   *Value `json:"isolationKey,omitempty"`
}

// WhileSpec bounds a durable loop. MaxIterations is a Value — symmetric with
// Initial — so a literal `20` keeps compiling unchanged while a `$ref` lets the
// bound come from the loop's own data (the scheduler re-resolves it per pass).
type WhileSpec struct {
	Condition     string `json:"condition"`
	MaxIterations Value  `json:"maxIterations"`
	Initial       Value  `json:"initial"`
}

// HandlerSpec is a bounded activity or subflow invoked by the runtime for a
// step failure, cancellation, or reverse-order compensation. It is data, not
// an embedded callback, and is compiled and pinned like every normal step.
type HandlerSpec struct {
	Uses string           `json:"uses"`
	With map[string]Value `json:"with,omitempty"`
}

type StepSpec struct {
	ID         string           `json:"id"`
	Uses       string           `json:"uses"`
	With       map[string]Value `json:"with,omitempty"`
	If         string           `json:"if,omitempty"`
	Foreach    *ForeachSpec     `json:"foreach,omitempty"`
	While      *WhileSpec       `json:"while,omitempty"`
	Retry      string           `json:"retry,omitempty"`
	OnError    *HandlerSpec     `json:"onError,omitempty"`
	OnCancel   *HandlerSpec     `json:"onCancel,omitempty"`
	Compensate *HandlerSpec     `json:"compensate,omitempty"`
}

type Ref struct{ Path string }

// Value is either a JSON literal/composite or one structured reference.
type Value struct {
	Ref     *Ref
	Literal any
}

func (v *Value) UnmarshalJSON(raw []byte) error {
	var probe map[string]json.RawMessage
	if len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal(raw, &probe); err != nil {
			return err
		}
		if refRaw, ok := probe["$ref"]; ok {
			if len(probe) != 1 {
				return fmt.Errorf("$ref object cannot contain other fields")
			}
			var path string
			if err := json.Unmarshal(refRaw, &path); err != nil || path == "" {
				return fmt.Errorf("$ref must be a non-empty string")
			}
			if err := validateRefSyntax(path); err != nil {
				return err
			}
			v.Ref = &Ref{Path: path}
			v.Literal = nil
			return nil
		}
	}
	literal, err := decodeLiteral(raw)
	if err != nil {
		return err
	}
	v.Literal = literal
	v.Ref = nil
	return nil
}

func decodeLiteral(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty JSON value")
	}
	switch raw[0] {
	case '{':
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		out := make(map[string]Value, len(fields))
		for key, field := range fields {
			var value Value
			if err := json.Unmarshal(field, &value); err != nil {
				return nil, fmt.Errorf("field %s: %w", key, err)
			}
			out[key] = value
		}
		return out, nil
	case '[':
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			return nil, err
		}
		out := make([]Value, len(elements))
		for index, element := range elements {
			if err := json.Unmarshal(element, &out[index]); err != nil {
				return nil, fmt.Errorf("index %d: %w", index, err)
			}
		}
		return out, nil
	default:
		var literal any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&literal); err != nil {
			return nil, err
		}
		return literal, nil
	}
}

func (v Value) MarshalJSON() ([]byte, error) {
	if v.Ref != nil {
		return json.Marshal(map[string]string{"$ref": v.Ref.Path})
	}
	return json.Marshal(v.Literal)
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
var versionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){0,2}(?:[-+][A-Za-z0-9.-]+)?$`)

func validateRefSyntax(path string) error {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return fmt.Errorf("invalid empty reference")
	}
	switch parts[0] {
	case "inputs", "steps", "item", "index", "attempt", "run", "config":
	default:
		return fmt.Errorf("reference %q has unknown root %q", path, parts[0])
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("reference %q contains an empty segment", path)
		}
	}
	return nil
}
