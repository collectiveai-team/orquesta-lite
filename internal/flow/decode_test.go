package flow

import (
	"strings"
	"testing"
)

const validFlow = `{
  "apiVersion":"orq.dev/v2",
  "kind":"Flow",
  "metadata":{"name":"demo","version":"1.0.0"},
  "inputs":{"name":{"schema":"schema:string@1"}},
  "steps":[{"id":"echo","uses":"activity:test.echo@1","with":{"value":{"$ref":"inputs.name"}}}],
  "outputs":{"value":{"$ref":"steps.echo.output.value"}}
}`

func TestDecodeStrictV2(t *testing.T) {
	doc, err := Decode(strings.NewReader(validFlow))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Metadata.Name != "demo" || doc.Steps[0].With["value"].Ref.Path != "inputs.name" {
		t.Fatalf("unexpected document: %+v", doc)
	}
}

func TestDecodeRejectsUnknownAndDuplicate(t *testing.T) {
	unknown := strings.Replace(validFlow, `"kind":"Flow"`, `"kind":"Flow","surprise":true`, 1)
	if _, err := Decode(strings.NewReader(unknown)); err == nil {
		t.Fatal("expected unknown field error")
	}
	duplicate := strings.Replace(validFlow, `]`, `,{"id":"echo","uses":"activity:test.echo@1"}]`, 1)
	if _, err := Decode(strings.NewReader(duplicate)); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestValueRejectsMixedRef(t *testing.T) {
	bad := strings.Replace(validFlow, `{"$ref":"inputs.name"}`, `{"$ref":"inputs.name","fallback":"x"}`, 1)
	if _, err := Decode(strings.NewReader(bad)); err == nil {
		t.Fatal("expected mixed ref error")
	}
}
