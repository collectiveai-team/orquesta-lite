package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
)

// Types accepts the JSON Schema string and string-array forms.
type Types []string

func (t *Types) UnmarshalJSON(raw []byte) error {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		*t = Types{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil || len(many) == 0 {
		return fmt.Errorf("schema type must be a string or non-empty string array")
	}
	*t = many
	return nil
}

// Schema is the deliberately small, fail-closed JSON Schema subset supported
// by the runtime MVP.
type Schema struct {
	Dialect              string             `json:"$schema,omitempty"`
	Title                string             `json:"title,omitempty"`
	Type                 Types              `json:"type,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
}

func DecodeSchema(r io.Reader) (*Schema, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var schema Schema
	if err := dec.Decode(&schema); err != nil {
		return nil, fmt.Errorf("schema: decode: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("schema: trailing JSON value")
	}
	if err := schema.validateDefinition("$"); err != nil {
		return nil, err
	}
	return &schema, nil
}

func (s *Schema) validateDefinition(path string) error {
	allowed := map[string]bool{"null": true, "boolean": true, "object": true, "array": true, "number": true, "integer": true, "string": true}
	for _, typ := range s.Type {
		if !allowed[typ] {
			return fmt.Errorf("schema %s: unsupported type %q", path, typ)
		}
	}
	for _, name := range s.Required {
		if _, ok := s.Properties[name]; !ok {
			return fmt.Errorf("schema %s: required property %q is not declared", path, name)
		}
	}
	for name, child := range s.Properties {
		if child == nil {
			return fmt.Errorf("schema %s.%s: nil schema", path, name)
		}
		if err := child.validateDefinition(path + "." + name); err != nil {
			return err
		}
	}
	if s.Items != nil {
		return s.Items.validateDefinition(path + "[]")
	}
	return nil
}

func (s *Schema) ValidateJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return s.Validate(value)
}

func (s *Schema) Validate(value any) error { return s.validateValue("$", value) }

func (s *Schema) validateValue(path string, value any) error {
	if len(s.Type) > 0 && !matchesAnyType(s.Type, value) {
		return fmt.Errorf("%s: expected %s, got %T", path, strings.Join(s.Type, "|"), value)
	}
	if len(s.Enum) > 0 {
		found := false
		for _, allowed := range s.Enum {
			if valuesEqual(allowed, value) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: value is not in enum", path)
		}
	}
	if s.Minimum != nil || s.Maximum != nil {
		number, ok := numericValue(value)
		if !ok {
			return fmt.Errorf("%s: expected a number to compare against minimum/maximum, got %T", path, value)
		}
		if s.Minimum != nil && number < *s.Minimum {
			return fmt.Errorf("%s: requires value >= %v", path, *s.Minimum)
		}
		if s.Maximum != nil && number > *s.Maximum {
			return fmt.Errorf("%s: requires value <= %v", path, *s.Maximum)
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, name := range s.Required {
			if _, ok := typed[name]; !ok {
				return fmt.Errorf("%s: missing required property %q", path, name)
			}
		}
		for name, childValue := range typed {
			childSchema, known := s.Properties[name]
			if !known {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("%s: unknown property %q", path, name)
				}
				continue
			}
			if err := childSchema.validateValue(path+"."+name, childValue); err != nil {
				return err
			}
		}
	case []any:
		if s.MinItems != nil && len(typed) < *s.MinItems {
			return fmt.Errorf("%s: requires at least %d items", path, *s.MinItems)
		}
		if s.Items != nil {
			for index, item := range typed {
				if err := s.Items.validateValue(fmt.Sprintf("%s[%d]", path, index), item); err != nil {
					return err
				}
			}
		}
	case string:
		if s.MinLength != nil && len([]rune(typed)) < *s.MinLength {
			return fmt.Errorf("%s: requires length >= %d", path, *s.MinLength)
		}
	}
	return nil
}

func matchesAnyType(types Types, value any) bool {
	for _, typ := range types {
		switch typ {
		case "null":
			if value == nil {
				return true
			}
		case "boolean":
			_, ok := value.(bool)
			if ok {
				return true
			}
		case "object":
			_, ok := value.(map[string]any)
			if ok {
				return true
			}
		case "array":
			_, ok := value.([]any)
			if ok {
				return true
			}
		case "string":
			_, ok := value.(string)
			if ok {
				return true
			}
		case "number":
			if isNumber(value) {
				return true
			}
		case "integer":
			if isInteger(value) {
				return true
			}
		}
	}
	return false
}

// numericValue widens any JSON-decoded number (json.Number under UseNumber, or
// a plain Go number) to float64 for range comparison.
func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func isNumber(value any) bool {
	switch value.(type) {
	case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func isInteger(value any) bool {
	_, ok := WholeNumber(value)
	return ok
}

// WholeNumber reports whether value is a JSON number with no fractional part,
// and returns it. It is the engine's single "is this an integer" predicate:
// schema validation of `"type":"integer"` and the while-loop's iteration bound
// both go through it, so a value cannot be legal to one and fatal to the other.
//
// The asymmetry it exists to erase is json.Number's. `json.Number("8.0").Int64()`
// fails — strconv.ParseInt rejects the decimal point — while `Float64()` returns
// 8, and a plain `float64(8.0)` from a non-UseNumber decode passes any
// `typed == float64(int64(typed))` check. A predicate built on Int64 alone
// therefore accepts or rejects the very same semantic value depending only on
// how the document happened to be decoded. Durable workflow state is decoded
// with UseNumber and an LLM writing a whole number with a trailing `.0` is
// entirely ordinary, so that is a live failure and not a curiosity: it killed
// governed runs whose planner emitted `"iteration_budget": 8.0`, a value the
// pinned schema itself accepted.
func WholeNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		if number, err := typed.Int64(); err == nil {
			return number, true
		}
		// Fall back to the float parse so "8.0" is the integer 8, and reject
		// anything that is not exactly representable as one.
		number, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return wholeFloat(number)
	case float64:
		return wholeFloat(typed)
	case float32:
		return wholeFloat(float64(typed))
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return unsignedWhole(uint64(typed))
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		return unsignedWhole(typed)
	default:
		return 0, false
	}
}

func wholeFloat(number float64) (int64, bool) {
	truncated := int64(number)
	if number != float64(truncated) {
		return 0, false
	}
	return truncated, true
}

func unsignedWhole(number uint64) (int64, bool) {
	if number > math.MaxInt64 {
		return 0, false
	}
	return int64(number), true
}

func valuesEqual(a, b any) bool {
	if isNumber(a) && isNumber(b) {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	return reflect.DeepEqual(a, b)
}
