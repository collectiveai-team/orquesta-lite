// Package telemetry persists measured usage separately from estimated prices
// and subscription utilization. Missing measurements remain explicit.
package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/cost"
)

var PricingVersion = cost.PricingVersion()

type Call struct {
	Version        int             `json:"version"`
	ID             string          `json:"id"`
	Time           time.Time       `json:"time"`
	Task           string          `json:"task"`
	Role           string          `json:"role"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	PrefixHash     string          `json:"prefix_hash,omitempty"`
	Granularity    string          `json:"granularity"`
	Input          int             `json:"input"`
	CacheRead      int             `json:"cache_read"`
	CacheWrite     int             `json:"cache_write"`
	Output         int             `json:"output"`
	Available      map[string]bool `json:"available"`
	Original       map[string]int  `json:"original_usage,omitempty"`
	EstimatedUSD   *float64        `json:"estimated_usd"`
	PricingVersion string          `json:"pricing_version"`
}

func NewCall(id, task, role, provider, model, prefix, granularity string, usage map[string]int) Call {
	c := Call{Version: 1, ID: id, Time: time.Now().UTC(), Task: task, Role: role, Provider: provider, Model: model, PrefixHash: prefix, Granularity: granularity, Original: usage, Available: map[string]bool{}, PricingVersion: PricingVersion}
	for _, field := range []struct {
		name string
		keys []string
		dst  *int
	}{{"input", []string{"input_tokens"}, &c.Input}, {"cache_read", []string{"cached_input_tokens", "cache_read_tokens"}, &c.CacheRead}, {"cache_write", []string{"cache_creation_input_tokens", "cache_write_tokens"}, &c.CacheWrite}, {"output", []string{"output_tokens"}, &c.Output}} {
		for _, key := range field.keys {
			if value, ok := usage[key]; ok {
				c.Available[field.name] = true
				*field.dst += value
			}
		}
	}
	if usd, ok := cost.EstimateUsageUSD(model, cost.Usage{Input: c.Input, CacheRead: c.CacheRead, CacheWrite: c.CacheWrite, Output: c.Output}); ok {
		c.EstimatedUSD = &usd
	}
	return c
}

type Report struct {
	Version                       int      `json:"version"`
	Records                       int      `json:"records"`
	MeasuredRecords               int      `json:"measured_records"`
	Input                         int      `json:"input"`
	CacheRead                     int      `json:"cache_read"`
	CacheWrite                    int      `json:"cache_write"`
	Output                        int      `json:"output"`
	TotalVolume                   int      `json:"total_volume"`
	CacheHitRate                  *float64 `json:"cache_hit_rate"`
	CacheHitFormula               string   `json:"cache_hit_formula"`
	ReuseRegressionTokens         int      `json:"reuse_regression_tokens"`
	ReuseRegressionEstimatedUSD   *float64 `json:"reuse_regression_estimated_usd"`
	EstimatedUSD                  float64  `json:"estimated_usd"`
	UnpricedRecords               int      `json:"unpriced_records"`
	PricingVersion                string   `json:"pricing_version"`
	PricingVersions               []string `json:"pricing_versions"`
	ReuseRegressionPricingVersion string   `json:"reuse_regression_pricing_version,omitempty"`
	ReuseRegressionUnpricedTokens int      `json:"reuse_regression_unpriced_tokens"`
	Limitations                   []string `json:"limitations"`
}

func Summarize(calls []Call) Report {
	r := Report{Version: 1, PricingVersion: PricingVersion, CacheHitFormula: "cache_read / (input + cache_read + cache_write), measured compatible records only", Limitations: []string{"reuse regressions are counterfactual estimates, not proven cache misses", "token volume is distinct from provider subscription window utilization", "CLI events may expose invocation totals rather than individual API calls"}}
	previous := map[string]Call{}
	var hitNumerator, hitDenominator int
	var regressionCost float64
	pricedRegression := false
	seen := map[string]bool{}
	versions := map[string]bool{}
	for _, c := range calls {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		r.Records++
		version := c.PricingVersion
		if version == "" {
			version = "unversioned"
		}
		versions[version] = true
		if len(c.Available) > 0 {
			r.MeasuredRecords++
		}
		r.Input += c.Input
		r.CacheRead += c.CacheRead
		r.CacheWrite += c.CacheWrite
		r.Output += c.Output
		if c.EstimatedUSD != nil {
			r.EstimatedUSD += *c.EstimatedUSD
		} else {
			r.UnpricedRecords++
		}
		if c.Available["input"] && c.Available["cache_read"] {
			hitNumerator += c.CacheRead
			hitDenominator += c.Input + c.CacheRead + c.CacheWrite
		}
		if c.PrefixHash == "" || !c.Available["cache_read"] {
			continue
		}
		key := c.Provider + "\x00" + c.Model + "\x00" + c.PrefixHash + "\x00" + c.Granularity + "\x00" + c.PricingVersion
		if p, ok := previous[key]; ok {
			drop := p.CacheRead - c.CacheRead
			if drop >= 1024 {
				r.ReuseRegressionTokens += drop
				usd, ok := cost.CacheReadRegressionUSD(c.Model, drop)
				if ok && c.PricingVersion == PricingVersion {
					regressionCost += usd
					pricedRegression = true
				} else {
					r.ReuseRegressionUnpricedTokens += drop
				}
			}
		}
		previous[key] = c
	}
	r.TotalVolume = r.Input + r.CacheRead + r.CacheWrite + r.Output
	if hitDenominator > 0 {
		rate := float64(hitNumerator) / float64(hitDenominator)
		r.CacheHitRate = &rate
	}
	if pricedRegression {
		r.ReuseRegressionEstimatedUSD = &regressionCost
		r.ReuseRegressionPricingVersion = PricingVersion
	}
	r.PricingVersions = []string{}
	for version := range versions {
		r.PricingVersions = append(r.PricingVersions, version)
	}
	sort.Strings(r.PricingVersions)
	if len(r.PricingVersions) == 1 {
		r.PricingVersion = r.PricingVersions[0]
	} else if len(r.PricingVersions) > 1 {
		r.PricingVersion = "mixed"
		r.Limitations = append(r.Limitations, "historical estimated costs retain their recorded pricing versions; mixed versions are not repriced")
	}
	if r.ReuseRegressionUnpricedTokens > 0 {
		r.Limitations = append(r.Limitations, "cache regression USD omits tokens without this embedded pricing version; historical rates are not guessed")
	}
	return r
}

var appendMu sync.Mutex

// Append writes an append-only journal and atomically replaces its derived
// report. The mutex also protects concurrent roles within the same process.
func Append(root string, calls []Call) error {
	if root == "" {
		return nil
	}
	appendMu.Lock()
	defer appendMu.Unlock()
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "usage.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, c := range calls {
		if err = enc.Encode(c); err != nil {
			f.Close()
			return err
		}
	}
	if err = f.Close(); err != nil {
		return err
	}
	all, err := Read(filepath.Join(root, "usage.jsonl"))
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(Summarize(all), "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, "usage-report-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(root, "usage-report.json"))
}

func Read(path string) ([]Call, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1<<20)
	var calls []Call
	for s.Scan() {
		var c Call
		if err := json.Unmarshal(s.Bytes(), &c); err != nil {
			return nil, fmt.Errorf("usage journal: %w", err)
		}
		calls = append(calls, c)
	}
	return calls, s.Err()
}
