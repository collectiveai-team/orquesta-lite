// Package promptbudget measures only the prefix controlled by orq-lite. CLI
// system instructions and tool schemas are explicitly outside its coverage.
package promptbudget

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

type Policy struct {
	MaxTokens        int     `json:"max_tokens"`
	BaselineTokens   int     `json:"baseline_tokens,omitempty"`
	MaxGrowthPercent float64 `json:"max_growth_percent,omitempty"`
	Justification    string  `json:"justification,omitempty"`
}

func (p Policy) Validate() error {
	if p.MaxTokens <= 0 || p.BaselineTokens < 0 || p.MaxGrowthPercent < 0 {
		return fmt.Errorf("prompt budget requires positive max_tokens and nonnegative baseline/growth")
	}
	if p.MaxGrowthPercent > 0 && p.BaselineTokens == 0 {
		return fmt.Errorf("growth policy requires baseline_tokens")
	}
	return nil
}

type Measurement struct {
	Tokens     int    `json:"tokens_upper_bound"`
	PrefixHash string `json:"prefix_hash"`
	Method     string `json:"method"`
	Coverage   string `json:"coverage"`
}

// Measure uses one token per UTF-8 byte as a conservative upper bound rather
// than presenting a tokenizer estimate as an exact provider measurement.
func Measure(prefix string) Measurement {
	return Measurement{len([]byte(prefix)), fmt.Sprintf("%x", sha256.Sum256([]byte(prefix))), "utf8_bytes_upper_bound", "orq_template_skills_conventions_only"}
}

func (p Policy) Check(m Measurement) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if m.Tokens > p.MaxTokens {
		return fmt.Errorf("controlled prompt prefix upper bound %d exceeds budget %d", m.Tokens, p.MaxTokens)
	}
	if p.BaselineTokens > 0 && float64(m.Tokens) > float64(p.BaselineTokens)*(1+p.MaxGrowthPercent/100) && strings.TrimSpace(p.Justification) == "" {
		return fmt.Errorf("controlled prompt prefix grew beyond baseline %d + %.2f%% without justification", p.BaselineTokens, p.MaxGrowthPercent)
	}
	return nil
}

// Assemble keeps stable instructions first. Every volatile substitution is
// replaced with a named reference and emitted in the final context block.
func Assemble(template string, stable, dynamic map[string]string) (string, Measurement) {
	keys := sorted(stable)
	var stableTail strings.Builder
	for _, key := range keys {
		placeholder := "{{" + key + "}}"
		if strings.Contains(template, placeholder) {
			template = strings.ReplaceAll(template, placeholder, stable[key])
		} else if strings.TrimSpace(stable[key]) != "" {
			fmt.Fprintf(&stableTail, "%s:\n%s\n", key, stable[key])
		}
	}
	for _, key := range sorted(dynamic) {
		template = strings.ReplaceAll(template, "{{"+key+"}}", "[context:"+key+"]")
	}
	if stableTail.Len() > 0 {
		template += "\n\n<role_context>\n" + stableTail.String() + "</role_context>\n"
	}
	m := Measure(template)
	var tail strings.Builder
	tail.WriteString(template)
	tail.WriteString("\n\n<run_context>\n")
	for _, key := range sorted(dynamic) {
		fmt.Fprintf(&tail, "%s:\n%s\n", key, dynamic[key])
	}
	tail.WriteString("</run_context>\n")
	return tail.String(), m
}

func sorted(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
