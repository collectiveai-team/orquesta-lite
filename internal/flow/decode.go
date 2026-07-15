package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const maxFlowDocumentBytes = 8 << 20

func Decode(r io.Reader) (*Document, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxFlowDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("flow: read: %w", err)
	}
	if len(raw) > maxFlowDocumentBytes {
		return nil, fmt.Errorf("flow: document exceeds %d bytes", maxFlowDocumentBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc Document
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("flow: decode: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("flow: decode: trailing JSON value")
	}
	if err := validateDocument(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func Load(path string) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("flow: load %s: %w", path, err)
	}
	defer f.Close()
	doc, err := Decode(f)
	if err != nil {
		return nil, fmt.Errorf("flow: load %s: %w", path, err)
	}
	return doc, nil
}

func validateDocument(doc *Document) error {
	if doc.APIVersion != APIVersionV2 {
		return fmt.Errorf("flow: unsupported apiVersion %q", doc.APIVersion)
	}
	if doc.Kind != KindFlow && doc.Kind != KindSubflow {
		return fmt.Errorf("flow: kind must be Flow or Subflow")
	}
	if !identifier.MatchString(doc.Metadata.Name) {
		return fmt.Errorf("flow: invalid metadata.name %q", doc.Metadata.Name)
	}
	if !versionPattern.MatchString(doc.Metadata.Version) {
		return fmt.Errorf("flow: invalid metadata.version %q", doc.Metadata.Version)
	}
	seen := map[string]bool{}
	nodes := 0
	checkValue := func(path string, value Value) error {
		if err := validateValueBudget(value, 0, &nodes); err != nil {
			return fmt.Errorf("flow: %s: %w", path, err)
		}
		return nil
	}
	for name, input := range doc.Inputs {
		if input.Default != nil {
			if err := checkValue("inputs."+name+".default", *input.Default); err != nil {
				return err
			}
		}
	}
	for i, step := range doc.Steps {
		if !identifier.MatchString(step.ID) {
			return fmt.Errorf("flow: steps[%d]: invalid id %q", i, step.ID)
		}
		if seen[step.ID] {
			return fmt.Errorf("flow: duplicate step id %q", step.ID)
		}
		seen[step.ID] = true
		if step.Uses == "" {
			return fmt.Errorf("flow: step %q: uses is required", step.ID)
		}
		if step.Foreach != nil && step.Foreach.As != "" && !identifier.MatchString(step.Foreach.As) {
			return fmt.Errorf("flow: step %q: invalid foreach.as %q", step.ID, step.Foreach.As)
		}
		if step.Foreach != nil && step.While != nil {
			return fmt.Errorf("flow: step %q cannot declare both foreach and while", step.ID)
		}
		if step.Foreach != nil && step.Foreach.MaxConcurrency < 0 {
			return fmt.Errorf("flow: step %q: maxConcurrency cannot be negative", step.ID)
		}
		if step.Foreach != nil && step.Foreach.MaxConcurrency > 1 && step.Foreach.IsolationKey == nil {
			return fmt.Errorf("flow: step %q: parallel foreach requires isolationKey", step.ID)
		}
		if step.While != nil && (step.While.Condition == "" || step.While.MaxIterations < 1) {
			return fmt.Errorf("flow: step %q: while requires condition and maxIterations > 0", step.ID)
		}
		for name, handler := range map[string]*HandlerSpec{"onError": step.OnError, "onCancel": step.OnCancel, "compensate": step.Compensate} {
			if handler != nil && handler.Uses == "" {
				return fmt.Errorf("flow: step %q: %s.uses is required", step.ID, name)
			}
			if handler != nil {
				for key, value := range handler.With {
					if err := checkValue(fmt.Sprintf("steps[%d].%s.with.%s", i, name, key), value); err != nil {
						return err
					}
				}
			}
		}
		for key, value := range step.With {
			if err := checkValue(fmt.Sprintf("steps[%d].with.%s", i, key), value); err != nil {
				return err
			}
		}
		if step.Foreach != nil {
			if err := checkValue(fmt.Sprintf("steps[%d].foreach.items", i), step.Foreach.Items); err != nil {
				return err
			}
			if step.Foreach.IsolationKey != nil {
				if err := checkValue(fmt.Sprintf("steps[%d].foreach.isolationKey", i), *step.Foreach.IsolationKey); err != nil {
					return err
				}
			}
		}
		if step.While != nil {
			if err := checkValue(fmt.Sprintf("steps[%d].while.initial", i), step.While.Initial); err != nil {
				return err
			}
		}
	}
	for name, value := range doc.Outputs {
		if err := checkValue("outputs."+name, value); err != nil {
			return err
		}
	}
	return nil
}

func validateValueBudget(value Value, depth int, nodes *int) error {
	*nodes++
	if depth > 64 {
		return fmt.Errorf("value nesting exceeds 64 levels")
	}
	if *nodes > 100000 {
		return fmt.Errorf("document contains too many value nodes")
	}
	switch literal := value.Literal.(type) {
	case map[string]Value:
		for _, child := range literal {
			if err := validateValueBudget(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case []Value:
		for _, child := range literal {
			if err := validateValueBudget(child, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}
