package flow

import (
	"encoding/json"
	"fmt"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

type MemoryCatalog struct {
	Documents  map[string]*Document
	Schemas    map[string]*Schema
	Policies   map[string]json.RawMessage
	Activities map[string]activity.Spec
}

func NewMemoryCatalog() *MemoryCatalog {
	return &MemoryCatalog{
		Documents:  map[string]*Document{},
		Schemas:    map[string]*Schema{},
		Policies:   map[string]json.RawMessage{},
		Activities: map[string]activity.Spec{},
	}
}

func (c *MemoryCatalog) ResolveDocument(ref ResourceRef) (*Document, Digest, error) {
	doc, ok := c.Documents[ref.String()]
	if !ok {
		return nil, "", fmt.Errorf("catalog: %s not found", ref)
	}
	raw, _ := json.Marshal(doc)
	return doc, digestBytes(raw), nil
}

func (c *MemoryCatalog) ResolveSchema(ref ResourceRef) (*Schema, Digest, error) {
	schema, ok := c.Schemas[ref.String()]
	if !ok {
		return nil, "", fmt.Errorf("catalog: %s not found", ref)
	}
	raw, _ := json.Marshal(schema)
	return schema, digestBytes(raw), nil
}

func (c *MemoryCatalog) ResolvePolicy(ref ResourceRef) (json.RawMessage, Digest, error) {
	raw, ok := c.Policies[ref.String()]
	if !ok {
		return nil, "", fmt.Errorf("catalog: %s not found", ref)
	}
	return append(json.RawMessage(nil), raw...), digestBytes(raw), nil
}

func (c *MemoryCatalog) ResolveActivity(ref ResourceRef) (activity.Spec, error) {
	spec, ok := c.Activities[ref.String()]
	if !ok {
		return activity.Spec{}, fmt.Errorf("catalog: %s not found", ref)
	}
	return spec, nil
}
