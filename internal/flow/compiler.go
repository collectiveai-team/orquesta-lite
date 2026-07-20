package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type Diagnostic struct{ Severity, Path, Message string }
type Diagnostics []Diagnostic

func (d Diagnostics) HasErrors() bool {
	for _, item := range d {
		if item.Severity == "error" {
			return true
		}
	}
	return false
}

func Compile(doc *Document, catalog Catalog) (*IR, Diagnostics) {
	return compileDocument(doc, catalog, map[string]bool{})
}

func compileDocument(doc *Document, catalog Catalog, stack map[string]bool) (*IR, Diagnostics) {
	key := string(doc.Kind) + ":" + doc.Metadata.Name + "@" + doc.Metadata.Version
	if stack[key] {
		return nil, Diagnostics{{"error", key, "cyclic subflow reference"}}
	}
	stack[key] = true
	defer delete(stack, key)
	ir := &IR{APIVersion: doc.APIVersion, Kind: doc.Kind, Metadata: doc.Metadata, Inputs: doc.Inputs, Outputs: doc.Outputs, Resources: map[string]Digest{}, Schemas: map[string]*Schema{}, Policies: map[string]json.RawMessage{}}
	knownSteps := map[string]bool{}
	stepSchemas := map[string]*Schema{}
	var diagnostics Diagnostics
	for name, input := range doc.Inputs {
		ref, err := ParseResourceRef(input.Schema)
		if err != nil || ref.Kind != "schema" {
			diagnostics = append(diagnostics, Diagnostic{"error", "inputs." + name + ".schema", "input schema must reference schema:name@version"})
			continue
		}
		schema, digest, err := catalog.ResolveSchema(ref)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{"error", "inputs." + name + ".schema", err.Error()})
			continue
		}
		ir.Schemas[ref.String()] = schema
		ir.Resources[ref.String()] = digest
	}
	validateValue := func(path string, value Value, allowItem bool) {
		err := walkRefs(value, func(ref string) error { return validateReference(ref, doc.Inputs, knownSteps, stepSchemas, allowItem) })
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{"error", path, err.Error()})
		}
	}
	compileHandler := func(path string, handler *HandlerSpec, allowItem bool) *IRHandler {
		if handler == nil {
			return nil
		}
		resource, err := ParseResourceRef(handler.Uses)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", err.Error()})
			return nil
		}
		compiled := &IRHandler{Uses: resource, With: handler.With}
		for name, value := range handler.With {
			validateValue(path+".with."+name, value, allowItem)
		}
		switch resource.Kind {
		case "activity":
			spec, resolveErr := catalog.ResolveActivity(resource)
			if resolveErr != nil {
				diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", resolveErr.Error()})
				return compiled
			}
			compiled.Activity = &spec
			for _, schemaRef := range []string{spec.InputSchema, spec.OutputSchema} {
				if schemaRef == "" {
					continue
				}
				ref, parseErr := ParseResourceRef(schemaRef)
				if parseErr != nil || ref.Kind != "schema" {
					diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", "activity schema must reference schema:name@version"})
					continue
				}
				schema, digest, schemaErr := catalog.ResolveSchema(ref)
				if schemaErr != nil {
					diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", schemaErr.Error()})
					continue
				}
				ir.Schemas[ref.String()] = schema
				ir.Resources[ref.String()] = digest
			}
			if spec.Name == "agent.invoke" {
				value, ok := handler.With["outputSchema"]
				schemaRef, literal := value.Literal.(string)
				if !ok || !literal {
					diagnostics = append(diagnostics, Diagnostic{"error", path + ".with.outputSchema", "agent.invoke requires a literal outputSchema ref"})
				} else if ref, parseErr := ParseResourceRef(schemaRef); parseErr != nil || ref.Kind != "schema" {
					diagnostics = append(diagnostics, Diagnostic{"error", path + ".with.outputSchema", "outputSchema must be schema:name@version"})
				} else if schema, digest, schemaErr := catalog.ResolveSchema(ref); schemaErr != nil {
					diagnostics = append(diagnostics, Diagnostic{"error", path + ".with.outputSchema", schemaErr.Error()})
				} else {
					ir.Schemas[ref.String()] = schema
					ir.Resources[ref.String()] = digest
				}
			}
		case "subflow":
			subdoc, digest, resolveErr := catalog.ResolveDocument(resource)
			if resolveErr != nil {
				diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", resolveErr.Error()})
				return compiled
			}
			if subdoc.Kind != KindSubflow {
				diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", "resolved document is not a Subflow"})
				return compiled
			}
			sub, subDiagnostics := compileDocument(subdoc, catalog, stack)
			diagnostics = append(diagnostics, subDiagnostics...)
			compiled.Subflow = sub
			ir.Resources[resource.String()] = digest
			if sub != nil {
				for schemaRef, schema := range sub.Schemas {
					ir.Schemas[schemaRef] = schema
				}
				for resolvedRef, resolvedDigest := range sub.Resources {
					ir.Resources[resolvedRef] = resolvedDigest
				}
				for policyRef, policy := range sub.Policies {
					ir.Policies[policyRef] = append(json.RawMessage(nil), policy...)
				}
			}
		default:
			diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", "handler uses must reference activity or subflow"})
		}
		return compiled
	}
	for index, step := range doc.Steps {
		path := fmt.Sprintf("steps[%d](%s)", index, step.ID)
		resource, err := ParseResourceRef(step.Uses)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", err.Error()})
			continue
		}
		compiled := IRStep{ID: step.ID, Uses: resource, With: step.With, If: step.If, Foreach: step.Foreach, While: step.While}
		if step.Foreach != nil {
			validateValue(path+".foreach.items", step.Foreach.Items, false)
			if step.Foreach.IsolationKey != nil {
				validateValue(path+".foreach.isolationKey", *step.Foreach.IsolationKey, true)
			}
		}
		if step.While != nil {
			validateValue(path+".while.initial", step.While.Initial, false)
		}
		for name, value := range step.With {
			validateValue(path+".with."+name, value, step.Foreach != nil || step.While != nil)
		}
		if step.Retry != "" {
			ref, err := ParseResourceRef(step.Retry)
			if err != nil || ref.Kind != "policy" {
				diagnostics = append(diagnostics, Diagnostic{"error", path + ".retry", "retry must reference policy:name@version"})
			} else {
				if rawPolicy, digest, resolveErr := catalog.ResolvePolicy(ref); resolveErr != nil {
					diagnostics = append(diagnostics, Diagnostic{"error", path + ".retry", resolveErr.Error()})
				} else {
					compiled.Retry = &ref
					ir.Resources[ref.String()] = digest
					ir.Policies[ref.String()] = append(json.RawMessage(nil), rawPolicy...)
				}
			}
		}
		var outputSchema *Schema
		switch resource.Kind {
		case "activity":
			spec, resolveErr := catalog.ResolveActivity(resource)
			if resolveErr != nil {
				diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", resolveErr.Error()})
			} else {
				compiled.Activity = &spec
				for _, schemaRef := range []string{spec.InputSchema, spec.OutputSchema} {
					if schemaRef == "" {
						continue
					}
					ref, parseErr := ParseResourceRef(schemaRef)
					if parseErr != nil || ref.Kind != "schema" {
						diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", "activity schema must reference schema:name@version"})
						continue
					}
					schema, digest, schemaErr := catalog.ResolveSchema(ref)
					if schemaErr != nil {
						diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", schemaErr.Error()})
						continue
					}
					ir.Schemas[ref.String()] = schema
					ir.Resources[ref.String()] = digest
					if schemaRef == spec.OutputSchema {
						outputSchema = schema
					}
				}
				if spec.Name == "agent.invoke" {
					if value, ok := step.With["outputSchema"]; ok {
						if schemaRef, ok := value.Literal.(string); ok {
							ref, parseErr := ParseResourceRef(schemaRef)
							if parseErr != nil || ref.Kind != "schema" {
								diagnostics = append(diagnostics, Diagnostic{"error", path + ".with.outputSchema", "outputSchema must be a literal schema:name@version ref"})
							} else if schema, digest, schemaErr := catalog.ResolveSchema(ref); schemaErr != nil {
								diagnostics = append(diagnostics, Diagnostic{"error", path + ".with.outputSchema", schemaErr.Error()})
							} else {
								ir.Schemas[ref.String()] = schema
								ir.Resources[ref.String()] = digest
							}
						} else {
							diagnostics = append(diagnostics, Diagnostic{"error", path + ".with.outputSchema", "agent.invoke outputSchema must be a literal ref"})
						}
					} else {
						diagnostics = append(diagnostics, Diagnostic{"error", path + ".with.outputSchema", "agent.invoke requires outputSchema"})
					}
				}
			}
		case "subflow":
			subdoc, digest, resolveErr := catalog.ResolveDocument(resource)
			if resolveErr != nil {
				diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", resolveErr.Error()})
				break
			}
			if subdoc.Kind != KindSubflow {
				diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", "resolved document is not a Subflow"})
				break
			}
			sub, subDiagnostics := compileDocument(subdoc, catalog, stack)
			diagnostics = append(diagnostics, subDiagnostics...)
			compiled.Subflow = sub
			ir.Resources[resource.String()] = digest
			if sub != nil {
				for schemaRef, schema := range sub.Schemas {
					ir.Schemas[schemaRef] = schema
				}
				for resolvedRef, resolvedDigest := range sub.Resources {
					ir.Resources[resolvedRef] = resolvedDigest
				}
				for policyRef, policy := range sub.Policies {
					ir.Policies[policyRef] = append(json.RawMessage(nil), policy...)
				}
			}
		default:
			diagnostics = append(diagnostics, Diagnostic{"error", path + ".uses", "step uses must reference activity or subflow"})
		}
		allowItem := step.Foreach != nil || step.While != nil
		compiled.OnError = compileHandler(path+".onError", step.OnError, allowItem)
		compiled.OnCancel = compileHandler(path+".onCancel", step.OnCancel, allowItem)
		compiled.Compensate = compileHandler(path+".compensate", step.Compensate, allowItem)
		ir.Steps = append(ir.Steps, compiled)
		knownSteps[step.ID] = true
		if outputSchema != nil && (step.Foreach != nil || step.While != nil) {
			outputSchema = &Schema{Type: Types{"array"}, Items: outputSchema}
		}
		stepSchemas[step.ID] = outputSchema
	}
	for name, value := range doc.Outputs {
		validateValue("outputs."+name, value, false)
	}
	if diagnostics.HasErrors() {
		return nil, diagnostics
	}
	raw, err := json.Marshal(ir)
	if err != nil {
		return nil, append(diagnostics, Diagnostic{"error", "ir", err.Error()})
	}
	ir.Digest = digestBytes(raw)
	return ir, diagnostics
}

func validateReference(ref string, inputs map[string]InputSpec, steps map[string]bool, stepSchemas map[string]*Schema, allowItem bool) error {
	parts := strings.Split(ref, ".")
	switch parts[0] {
	case "inputs":
		if len(parts) < 2 || inputs[parts[1]].Schema == "" {
			return fmt.Errorf("reference %q names unknown input", ref)
		}
	case "steps":
		if len(parts) < 3 || !steps[parts[1]] || parts[2] != "output" {
			return fmt.Errorf("reference %q must name a prior step output", ref)
		}
		if len(parts) > 3 {
			schema := stepSchemas[parts[1]]
			for _, segment := range parts[3:] {
				if schema == nil {
					break
				}
				child, ok := schema.Properties[segment]
				if !ok {
					if schema.AdditionalProperties != nil && !*schema.AdditionalProperties {
						return fmt.Errorf("reference %q names property %q rejected by the pinned output schema", ref, segment)
					}
					break
				}
				schema = child
			}
		}
	case "item", "index":
		if !allowItem {
			return fmt.Errorf("reference %q is only valid inside foreach", ref)
		}
	case "attempt", "run":
		// Runtime-owned read-only values.
	default:
		return fmt.Errorf("reference %q has unknown root", ref)
	}
	return nil
}

func digestBytes(raw []byte) Digest {
	sum := sha256.Sum256(raw)
	return Digest(hex.EncodeToString(sum[:]))
}
