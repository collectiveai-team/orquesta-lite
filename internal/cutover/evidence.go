// Package cutover implements the fail-closed deletion gate for the legacy
// development runtime. It is migration tooling: the package can be removed
// together with the compatibility runtime once every gate is satisfied.
package cutover

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lionelchamorro/orquestalite/internal/legacytest"
)

const APIVersion = "orq.cutover/v1"

var ErrGateClosed = errors.New("legacy runtime deletion gate is closed")

type Evidence struct {
	APIVersion         string              `json:"apiVersion"`
	RuntimeCommit      string              `json:"runtimeCommit"`
	Parity             []ParityResult      `json:"parity"`
	Benchmarks         []BenchmarkResult   `json:"benchmarks"`
	Chaos              []ChaosResult       `json:"chaos"`
	Canary             CanaryResult        `json:"canary"`
	Rollback           RollbackResult      `json:"rollback"`
	ControlledProjects []ControlledProject `json:"controlledProjects"`
	DevelopmentPack    DevelopmentPack     `json:"developmentPack"`
}

type ParityResult struct {
	Scenario       string `json:"scenario"`
	LegacyBaseline string `json:"legacyBaseline"`
	V2Passed       bool   `json:"v2Passed"`
	Commit         string `json:"commit"`
}

type BenchmarkResult struct {
	ID                  string `json:"id"`
	Commit              string `json:"commit"`
	Model               string `json:"model"`
	ConfigDigest        string `json:"configDigest"`
	Passed              bool   `json:"passed"`
	CriticalRegressions int    `json:"criticalRegressions"`
	CompletedAt         string `json:"completedAt"`
}

type ChaosResult struct {
	Scenario    string `json:"scenario"`
	Passed      bool   `json:"passed"`
	Commit      string `json:"commit"`
	CompletedAt string `json:"completedAt"`
}

type CanaryResult struct {
	Release     string `json:"release"`
	Commit      string `json:"commit"`
	Project     string `json:"project"`
	V2Default   bool   `json:"v2Default"`
	Passed      bool   `json:"passed"`
	CompletedAt string `json:"completedAt"`
}

type RollbackResult struct {
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	Project     string `json:"project"`
	Passed      bool   `json:"passed"`
	CompletedAt string `json:"completedAt"`
}

type ControlledProject struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type DevelopmentPack struct {
	Root           string `json:"root"`
	Name           string `json:"name"`
	Version        string `json:"version"`
	ManifestDigest string `json:"manifestDigest"`
}

type Gate struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type Report struct {
	Open  bool   `json:"open"`
	Gates []Gate `json:"gates"`
}

func Load(path string) (*Evidence, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cutover: read evidence: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var evidence Evidence
	if err = decoder.Decode(&evidence); err != nil {
		return nil, fmt.Errorf("cutover: decode evidence: %w", err)
	}
	if err = requireEOF(decoder); err != nil {
		return nil, err
	}
	if evidence.APIVersion != APIVersion {
		return nil, fmt.Errorf("cutover: unsupported apiVersion %q", evidence.APIVersion)
	}
	return &evidence, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("cutover: trailing JSON value")
		}
		return fmt.Errorf("cutover: trailing JSON: %w", err)
	}
	return nil
}

// Template returns a deliberately failing evidence document with every
// required scenario named. Release automation fills it with observed facts;
// the runtime never marks evidence as passed itself.
func Template() Evidence {
	parity := make([]ParityResult, 0, len(legacytest.Scenarios))
	for _, scenario := range legacytest.Scenarios {
		parity = append(parity, ParityResult{Scenario: scenario.Name})
	}
	chaos := make([]ChaosResult, 0, len(requiredChaosScenarios))
	for _, scenario := range requiredChaosScenarios {
		chaos = append(chaos, ChaosResult{Scenario: scenario})
	}
	return Evidence{
		APIVersion:         APIVersion,
		Parity:             parity,
		Benchmarks:         []BenchmarkResult{},
		Chaos:              chaos,
		ControlledProjects: []ControlledProject{},
	}
}
