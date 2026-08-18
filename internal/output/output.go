// Copyright 2026 Gabriel Harnagea
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package output renders the result of a `check` or `watch` execution in
// two formats (human-readable text by default, stable JSON for CI use).
// The renderer imports neither internal/check nor internal/diagnose: both
// convert their Result to output.Group, the minimum type needed to render.
// This is what makes "a single Report for both commands" possible without
// this package transitively pulling in client-go just for JSON consumers.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// Group is what check.Result and diagnose.Result have in common: the
// identifier of what produced the result, the Findings, and the distinction
// between "no issues" and "unable to evaluate". Callers (cmd/) convert
// their Results to Group with a simple field copy: it is deliberately a
// logic-free adapter, not an extension point.
type Group struct {
	ID         string
	Findings   []model.Finding
	Skipped    bool
	SkipReason string
}

// Report is the serializable form of a set of Groups, independent of check
// and diagnose so that JSON-only consumers are not forced to transitively
// import client-go.
type Report struct {
	Status  string        `json:"status,omitempty"`
	Results []CheckReport `json:"results"`
}

// CheckReport is the serializable form of a single Group. The JSON field
// name "checkId" remains unchanged even for Groups produced by `watch`
// (where the ID is a diagnose category, e.g. "crashloop"): changing it
// would break compatibility for existing consumers of `check` JSON output
// without a gain that would justify it.
type CheckReport struct {
	CheckID    string          `json:"checkId"`
	Skipped    bool            `json:"skipped"`
	SkipReason string          `json:"skipReason,omitempty"`
	Findings   []model.Finding `json:"findings"`
}

// NewReport converts raw Groups into a Report. Finding order is decided
// here, not in the caller: descending by severity, so human-readable output
// shows what matters most first, regardless of check or classification
// registration order.
func NewReport(groups []Group) Report {
	r := Report{Results: make([]CheckReport, 0, len(groups))}
	for _, g := range groups {
		findings := append([]model.Finding(nil), g.Findings...)
		sort.SliceStable(findings, func(i, j int) bool {
			return findings[i].Severity > findings[j].Severity
		})
		r.Results = append(r.Results, CheckReport{
			CheckID:    g.ID,
			Skipped:    g.Skipped,
			SkipReason: g.SkipReason,
			Findings:   findings,
		})
	}
	return r
}

// MaxSeverity returns the highest severity among all report findings, with
// ok=false if there are no findings. The command uses it to determine the
// exit code.
func (r Report) MaxSeverity() (sev model.Severity, ok bool) {
	for _, cr := range r.Results {
		for _, f := range cr.Findings {
			if !ok || f.Severity > sev {
				sev = f.Severity
				ok = true
			}
		}
	}
	return sev, ok
}

// RenderJSON writes the report as stable, indented JSON.
func RenderJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// RenderHuman writes the report in a text format designed to be read under
// pressure: highest severity and cause on the first line, with evidence and
// remediation indented below.
func RenderHuman(w io.Writer, r Report) error {
	var err error
	// printf retains the first write error instead of ignoring it: with an
	// io.Writer (e.g. a file on a full disk), fmt.Fprintf may fail, and
	// propagating that error makes this function reliable for --output json
	// and tests that intercept errors.
	printf := func(format string, a ...any) {
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(w, format, a...)
	}
	if r.Status == "succeeded" {
		printf("SUCCESS rollout completed\n")
		if len(r.Results) == 0 {
			return err
		}
	}

	anyFinding := false
	for _, cr := range r.Results {
		if cr.Skipped {
			printf("SKIP  %-24s %s\n", cr.CheckID, cr.SkipReason)
			continue
		}
		if len(cr.Findings) == 0 {
			printf("OK    %-24s no issues detected\n", cr.CheckID)
			continue
		}
		anyFinding = true
		for _, f := range cr.Findings {
			printf("%-5s %-24s %s\n", severityLabel(f.Severity), f.CheckID, f.Resource)
			if f.Undetermined {
				printf("      cause (undetermined): %s\n", f.Cause)
			} else {
				printf("      cause: %s\n", f.Cause)
			}
			for _, e := range f.Evidence {
				printf("      evidence: %s\n", e)
			}
			printf("      remediation: %s\n", f.Remediation.Summary)
			for _, cmd := range f.Remediation.Commands {
				printf("        $ %s\n", cmd)
			}
			if f.Remediation.ContextDependent {
				printf("      note: remediation is context dependent, review before applying\n")
			}
		}
	}
	if !anyFinding {
		printf("\nno findings of relevant severity\n")
	}
	return err
}

func severityLabel(s model.Severity) string {
	switch s {
	case model.SeverityHigh:
		return "HIGH"
	case model.SeverityMedium:
		return "MED"
	default:
		return "LOW"
	}
}
