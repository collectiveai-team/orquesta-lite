package flow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

type Digest string

type ResourceRef struct {
	Kind    string
	Name    string
	Version string
}

// DirectoryCatalog resolves a local, already-installed pack. It never uses the
// network. Versioned files (`name@1.json`) take precedence over `name.json`.
type DirectoryCatalog struct {
	Root     string
	Builtins map[string]activity.Spec
}

func NewDirectoryCatalog(root string, builtins []activity.Spec) *DirectoryCatalog {
	items := map[string]activity.Spec{}
	for _, spec := range builtins {
		items["activity:"+spec.Ref()] = spec
	}
	return &DirectoryCatalog{Root: root, Builtins: items}
}

func (c *DirectoryCatalog) ResolveDocument(ref ResourceRef) (*Document, Digest, error) {
	directory := "flows"
	expected := KindFlow
	if ref.Kind == "subflow" {
		directory = "subflows"
		expected = KindSubflow
	}
	if ref.Kind != "flow" && ref.Kind != "subflow" {
		return nil, "", fmt.Errorf("catalog: %s is not a document", ref)
	}
	path, raw, err := c.read(directory, ref)
	if err != nil {
		return nil, "", err
	}
	doc, err := Load(path)
	if err != nil {
		return nil, "", err
	}
	if doc.Kind != expected || doc.Metadata.Name != ref.Name || doc.Metadata.Version != ref.Version {
		return nil, "", fmt.Errorf("catalog: %s metadata does not match %s", path, ref)
	}
	return doc, digestBytes(raw), nil
}
func (c *DirectoryCatalog) ResolveSchema(ref ResourceRef) (*Schema, Digest, error) {
	path, raw, err := c.read("schemas", ref)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	schema, err := DecodeSchema(file)
	if err != nil {
		return nil, "", err
	}
	return schema, digestBytes(raw), nil
}
func (c *DirectoryCatalog) ResolvePolicy(ref ResourceRef) (json.RawMessage, Digest, error) {
	_, raw, err := c.read("policies", ref)
	if err != nil {
		return nil, "", err
	}
	return json.RawMessage(raw), digestBytes(raw), nil
}
func (c *DirectoryCatalog) ResolveActivity(ref ResourceRef) (activity.Spec, error) {
	if spec, ok := c.Builtins[ref.String()]; ok {
		return spec, nil
	}
	_, raw, err := c.read("activities", ref)
	if err != nil {
		return activity.Spec{}, err
	}
	var manifest struct {
		Spec activity.Spec `json:"spec"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return activity.Spec{}, err
	}
	if manifest.Spec.Name != ref.Name || manifest.Spec.Version != ref.Version {
		return activity.Spec{}, fmt.Errorf("catalog: activity manifest metadata mismatch for %s", ref)
	}
	return manifest.Spec, nil
}
func (c *DirectoryCatalog) read(directory string, ref ResourceRef) (string, []byte, error) {
	candidates := []string{filepath.Join(c.Root, directory, ref.Name+"@"+ref.Version+".json"), filepath.Join(c.Root, directory, ref.Name+".json")}
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err == nil {
			return path, raw, nil
		}
		if !os.IsNotExist(err) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("catalog: %s not found under %s", ref, c.Root)
}

// PackRootForPath returns the pack root for a document inside flows/ or
// subflows/, otherwise the document's directory.
func PackRootForPath(path string) string {
	dir := filepath.Dir(path)
	base := filepath.Base(dir)
	if base == "flows" || base == "subflows" {
		return filepath.Dir(dir)
	}
	return dir
}

func (r ResourceRef) String() string { return r.Kind + ":" + r.Name + "@" + r.Version }

func ParseResourceRef(raw string) (ResourceRef, error) {
	kind, rest, ok := strings.Cut(raw, ":")
	if !ok || kind == "" {
		return ResourceRef{}, fmt.Errorf("resource ref %q: missing kind", raw)
	}
	name, version, ok := strings.Cut(rest, "@")
	if !ok || name == "" || version == "" || strings.Contains(version, "@") {
		return ResourceRef{}, fmt.Errorf("resource ref %q: expected kind:name@version", raw)
	}
	switch kind {
	case "flow", "subflow", "schema", "policy", "activity":
	default:
		return ResourceRef{}, fmt.Errorf("resource ref %q: unknown kind %q", raw, kind)
	}
	return ResourceRef{Kind: kind, Name: name, Version: version}, nil
}

type Catalog interface {
	ResolveDocument(ResourceRef) (*Document, Digest, error)
	ResolveSchema(ResourceRef) (*Schema, Digest, error)
	ResolvePolicy(ResourceRef) (json.RawMessage, Digest, error)
	ResolveActivity(ResourceRef) (activity.Spec, error)
}
