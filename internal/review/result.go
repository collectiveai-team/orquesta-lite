// Package review defines the versioned, semantic review contract.
package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type Finding struct {
	ID         string   `json:"id"`
	Category   string   `json:"category"`
	Severity   string   `json:"severity"`
	State      string   `json:"state"`
	Location   string   `json:"location"`
	Trigger    string   `json:"trigger"`
	Impact     string   `json:"impact"`
	Evidence   []string `json:"evidence"`
	Origins    []string `json:"origins"`
	Resolution string   `json:"resolution"`
}

type Visual struct {
	Requested string   `json:"requested"`
	Executed  string   `json:"executed"`
	Required  []string `json:"required"`
	Checked   []string `json:"checked"`
	Pending   []string `json:"pending"`
	Reason    string   `json:"reason"`
	URL       string   `json:"url"`
	Artifacts []string `json:"artifacts"`
}

type Result struct {
	Version          int       `json:"version"`
	Status           string    `json:"status"`
	Decision         string    `json:"decision"`
	Approved         bool      `json:"approved"`
	Summary          string    `json:"summary"`
	Findings         []Finding `json:"findings"`
	Limitations      []string  `json:"limitations"`
	ReviewedRevision string    `json:"reviewed_revision"`
	Evidence         []string  `json:"evidence"`
	Visual           *Visual   `json:"visual,omitempty"`
}

func Decode(raw []byte) (Result, error) {
	var r Result
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	return r, r.Validate()
}

// Validate reports every violation in one error rather than the first, naming
// the finding or criterion each one is about and the values a field accepts.
// The corrective retry hands this text back to the agent, and a retry that sees
// one problem at a time spends its whole budget fixing them one by one.
func (r Result) Validate() error {
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	statuses := []string{"complete", "partial", "unavailable", "not_applicable"}
	decisions := []string{"approve", "warn", "block", "inconclusive"}
	if r.Version != 2 {
		bad("version must be 2, got %d", r.Version)
	}
	if !slices.Contains(statuses, r.Status) {
		bad("status %q is not one of %s", r.Status, strings.Join(statuses, ", "))
	}
	if !slices.Contains(decisions, r.Decision) {
		bad("decision %q is not one of %s", r.Decision, strings.Join(decisions, ", "))
	}
	if strings.TrimSpace(r.Summary) == "" || r.Findings == nil || r.Limitations == nil || r.Evidence == nil {
		bad("summary and explicit arrays required (findings, limitations, evidence may be [] but not missing)")
	}
	complete := r.Status == "complete" || r.Status == "not_applicable"
	if r.Approved != (complete && r.Decision == "approve") {
		bad("approved must agree with status and decision: true exactly when decision is approve and status is complete or not_applicable")
	}
	if !complete && (r.Decision == "approve" || r.Decision == "warn" || len(r.Limitations) == 0) {
		bad("incomplete review requires limitations and inconclusive/block")
	}
	if complete && strings.TrimSpace(r.ReviewedRevision) == "" {
		bad("completed review requires revision evidence in reviewed_revision")
	}
	if r.Status == "not_applicable" && (len(r.Evidence) == 0 || len(r.Limitations) == 0 || len(r.Findings) > 0) {
		bad("not_applicable requires scope evidence, justification and no defects")
	}
	severities := []string{"critical", "high", "medium", "low"}
	states := []string{"open", "resolved", "refuted"}
	seen := map[string]bool{}
	blocking := false
	open := false
	for i, f := range r.Findings {
		name := fmt.Sprintf("finding %q", f.ID)
		if f.ID == "" {
			name = fmt.Sprintf("finding #%d (no id)", i+1)
		}
		if f.ID == "" || seen[f.ID] || f.Category == "" || f.Location == "" || f.Trigger == "" || f.Impact == "" || len(f.Evidence) == 0 || len(f.Origins) == 0 {
			bad("%s requires unique ID, scenario, impact, evidence and origins%s", name, missingFields(f, seen[f.ID]))
		}
		seen[f.ID] = true
		if !slices.Contains(severities, f.Severity) {
			bad("%s: severity %q is not one of %s", name, f.Severity, strings.Join(severities, ", "))
		}
		if !slices.Contains(states, f.State) {
			bad("%s: state %q is not one of %s", name, f.State, strings.Join(states, ", "))
		}
		if f.State != "open" && strings.TrimSpace(f.Resolution) == "" {
			bad("%s: changed finding state requires resolution evidence", name)
		}
		if f.State == "open" {
			open = true
			if f.Severity != "low" {
				blocking = true
			}
		}
	}
	if blocking && r.Decision != "block" {
		bad("open blocking defect cannot disappear from decision: an open critical/high/medium finding requires decision block")
	}
	if r.Decision == "block" && !blocking {
		bad("block requires confirmed blocking defect; missing evidence is inconclusive")
	}
	if r.Decision == "approve" && open {
		bad("approval cannot carry open defects")
	}
	if r.Visual != nil {
		validateVisual(r, bad)
	}
	if len(problems) == 0 {
		return nil
	}
	if len(problems) == 1 {
		return fmt.Errorf("review-result@2: %s", problems[0])
	}
	return fmt.Errorf("review-result@2: %d problems:\n- %s", len(problems), strings.Join(problems, "\n- "))
}

func validateVisual(r Result, bad func(string, ...any)) {
	v := r.Visual
	requested := []string{"browser", "static", "not_applicable"}
	executed := []string{"browser", "static", "not_applicable", "unavailable"}
	if !slices.Contains(requested, v.Requested) {
		bad("invalid visual mode: visual.requested %q is not one of %s", v.Requested, strings.Join(requested, ", "))
	}
	if !slices.Contains(executed, v.Executed) {
		bad("invalid visual mode: visual.executed %q is not one of %s (name the tool in evidence, not here)", v.Executed, strings.Join(executed, ", "))
	}
	missing := false
	for _, c := range v.Required {
		if !slices.Contains(v.Checked, c) {
			missing = true
			if !slices.Contains(v.Pending, c) {
				bad("unchecked criterion must be pending: visual criterion %q is in required but in neither checked nor pending (copy the exact string)", c)
			}
		}
	}
	degraded := (v.Requested == "browser" && v.Executed != "browser") || missing || len(v.Pending) > 0
	if degraded && (r.Status == "complete" || r.Approved || v.Reason == "" || len(r.Limitations) == 0) {
		bad("degraded visual review must be incomplete with reason and limitations")
	}
	if v.Executed == "not_applicable" && (r.Status != "not_applicable" || v.Reason == "") {
		bad("visual not_applicable needs justified scope")
	}
}

func missingFields(f Finding, duplicate bool) string {
	var missing []string
	for name, empty := range map[string]bool{
		"id": f.ID == "", "category": f.Category == "", "location": f.Location == "",
		"trigger": f.Trigger == "", "impact": f.Impact == "", "evidence": len(f.Evidence) == 0, "origins": len(f.Origins) == 0,
	} {
		if empty {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	if duplicate {
		missing = append(missing, "id is duplicated")
	}
	if len(missing) == 0 {
		return ""
	}
	return " (missing: " + strings.Join(missing, ", ") + ")"
}

// Preserve checks historical identity. A reviewer may resolve/refute a defect
// explicitly with evidence, but cannot silently omit it or erase its sources.
// Like Validate, it reports every lost item at once.
func Preserve(current Result, previous []Result) error {
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, prior := range previous {
		if current.Approved && prior.Status != "complete" && prior.Status != "not_applicable" {
			bad("cannot approve with incomplete required review")
		}
		for _, old := range prior.Findings {
			if old.State != "open" || old.Severity == "low" {
				continue
			}
			i := slices.IndexFunc(current.Findings, func(f Finding) bool { return f.ID == old.ID })
			if i < 0 {
				bad("missing upstream blocking finding %s: carry it forward (resolve or refute it with evidence, never omit it)", old.ID)
				continue
			}
			f := current.Findings[i]
			for _, origin := range old.Origins {
				if !slices.Contains(f.Origins, origin) {
					bad("lost origin for %s: origin %q must stay in origins", old.ID, origin)
				}
			}
			for _, e := range old.Evidence {
				if !slices.Contains(f.Evidence, e) {
					bad("lost evidence for %s: copy this upstream evidence string verbatim into evidence: %q", old.ID, e)
				}
			}
			if f.State == "open" && old.Severity != "low" && f.Severity == "low" {
				bad("silently downgraded blocking finding %s", old.ID)
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	if len(problems) == 1 {
		return fmt.Errorf("%s", problems[0])
	}
	return fmt.Errorf("%d problems:\n- %s", len(problems), strings.Join(problems, "\n- "))
}

// Aggregate keeps duplicate IDs as one defect with all evidence and sources.
//
// Only the structured claims can disagree. Trigger and Impact are prose each
// reviewer writes in its own words, so comparing them byte for byte reported
// agreement as a conflict: four reviewers finding the same defect, wording its
// impact differently, failed the whole run and the findings never reached the
// repair loop. Their wording is kept as evidence instead.
//
// A real disagreement on state or severity resolves towards the more alarming
// claim rather than aborting a run whose tickets are already committed.
// Escalating can over-report a defect; last-writer-wins could drop one.
func Aggregate(reviews []Result) ([]Finding, bool, error) {
	if len(reviews) == 0 {
		return nil, false, fmt.Errorf("required reviews cannot be empty")
	}
	findings := []Finding{}
	ready := true
	for _, r := range reviews {
		if err := r.Validate(); err != nil {
			return nil, false, err
		}
		ready = ready && (r.Status == "complete" || r.Status == "not_applicable")
		for _, f := range r.Findings {
			i := slices.IndexFunc(findings, func(old Finding) bool { return old.ID == f.ID })
			if i < 0 {
				findings = append(findings, f)
				continue
			}
			merge(&findings[i], f)
		}
	}
	return findings, ready, nil
}

var severityRank = map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}

// merge folds a second report of the same defect into the one already kept.
func merge(old *Finding, f Finding) {
	if severityRank[f.Severity] > severityRank[old.Severity] {
		old.Severity = f.Severity
	}
	// An unresolved claim outranks a resolution: a defect one reviewer still
	// reproduces is open, whichever order the reviews arrived in.
	if f.State == "open" && old.State != "open" {
		old.State, old.Resolution = f.State, f.Resolution
	}
	// Each reviewer's own account of the defect is evidence for the repair
	// loop, which otherwise only ever sees whichever one happened to be first.
	for _, account := range []struct{ field, text string }{{"trigger", f.Trigger}, {"impact", f.Impact}} {
		if account.text == "" || account.text == old.Trigger || account.text == old.Impact {
			continue
		}
		keep(&old.Evidence, fmt.Sprintf("%s per %s: %s", account.field, strings.Join(f.Origins, ", "), account.text))
	}
	for _, x := range f.Origins {
		keep(&old.Origins, x)
	}
	for _, x := range f.Evidence {
		keep(&old.Evidence, x)
	}
}

func keep(list *[]string, value string) {
	if !slices.Contains(*list, value) {
		*list = append(*list, value)
	}
}
