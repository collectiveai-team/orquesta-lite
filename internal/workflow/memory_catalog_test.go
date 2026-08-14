package workflow

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
)

type memoryCatalog struct {
	Documents  map[string]*flow.Document
	Schemas    map[string]*flow.Schema
	Policies   map[string]json.RawMessage
	Activities map[string]activity.Spec
}

func newMemoryCatalog() *memoryCatalog {
	return &memoryCatalog{
		Documents:  map[string]*flow.Document{},
		Schemas:    map[string]*flow.Schema{},
		Policies:   map[string]json.RawMessage{},
		Activities: map[string]activity.Spec{},
	}
}

func testDigest(value any) flow.Digest {
	raw, _ := json.Marshal(value)
	return flow.Digest(fmt.Sprintf("%x", sha256.Sum256(raw)))
}

func (c *memoryCatalog) ResolveDocument(ref flow.ResourceRef) (*flow.Document, flow.Digest, error) {
	doc, ok := c.Documents[ref.String()]
	if !ok {
		return nil, "", fmt.Errorf("catalog: %s not found", ref)
	}
	return doc, testDigest(doc), nil
}

func (c *memoryCatalog) ResolveSchema(ref flow.ResourceRef) (*flow.Schema, flow.Digest, error) {
	schema, ok := c.Schemas[ref.String()]
	if !ok {
		return nil, "", fmt.Errorf("catalog: %s not found", ref)
	}
	return schema, testDigest(schema), nil
}

func (c *memoryCatalog) ResolvePolicy(ref flow.ResourceRef) (json.RawMessage, flow.Digest, error) {
	raw, ok := c.Policies[ref.String()]
	if !ok {
		return nil, "", fmt.Errorf("catalog: %s not found", ref)
	}
	return append(json.RawMessage(nil), raw...), testDigest(raw), nil
}

func (c *memoryCatalog) ResolveActivity(ref flow.ResourceRef) (activity.Spec, error) {
	spec, ok := c.Activities[ref.String()]
	if !ok {
		return activity.Spec{}, fmt.Errorf("catalog: %s not found", ref)
	}
	return spec, nil
}
