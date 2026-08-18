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

package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/output"
)

func sampleResults() []output.Group {
	return []output.Group{
		{
			ID: "pdb-consistency",
			Findings: []model.Finding{
				{
					CheckID:     "pdb-consistency",
					Severity:    model.SeverityLow,
					Cause:       "low-severity cause",
					Resource:    model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: "default", Name: "a"},
					Remediation: model.Remediation{Summary: "low-severity remediation"},
				},
				{
					CheckID:     "pdb-consistency",
					Severity:    model.SeverityHigh,
					Cause:       "high-severity cause",
					Resource:    model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: "default", Name: "b"},
					Remediation: model.Remediation{Summary: "high-severity remediation", ContextDependent: true},
				},
			},
		},
		{
			ID:         "quota-headroom",
			Skipped:    true,
			SkipReason: "ResourceQuota unavailable",
		},
		{
			ID:       "probe-sanity",
			Findings: nil,
		},
	}
}

func TestNewReport_SortsByDescendingSeverity(t *testing.T) {
	r := output.NewReport(sampleResults())

	findings := r.Results[0].Findings
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings in first CheckReport, got %d", len(findings))
	}
	if findings[0].Severity != model.SeverityHigh || findings[1].Severity != model.SeverityLow {
		t.Errorf("order is not descending by severity: %v then %v", findings[0].Severity, findings[1].Severity)
	}
}

func TestReport_MaxSeverity(t *testing.T) {
	r := output.NewReport(sampleResults())

	sev, ok := r.MaxSeverity()
	if !ok {
		t.Fatal("MaxSeverity() ok=false, want true: findings are present")
	}
	if sev != model.SeverityHigh {
		t.Errorf("MaxSeverity() = %v, want High", sev)
	}
}

func TestReport_MaxSeverity_NoFindings(t *testing.T) {
	r := output.NewReport([]output.Group{{ID: "ok-check"}})

	if _, ok := r.MaxSeverity(); ok {
		t.Error("MaxSeverity() ok=true with no findings, want false")
	}
}

func TestRenderJSON_RoundTrip(t *testing.T) {
	r := output.NewReport(sampleResults())

	var buf bytes.Buffer
	if err := output.RenderJSON(&buf, r); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	var decoded output.Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("produced JSON cannot be decoded: %v\noutput: %s", err, buf.String())
	}
	if len(decoded.Results) != len(r.Results) {
		t.Errorf("decoded Results = %d, want %d", len(decoded.Results), len(r.Results))
	}
}

func TestRenderJSON_SeverityAsString(t *testing.T) {
	r := output.NewReport(sampleResults())

	var buf bytes.Buffer
	if err := output.RenderJSON(&buf, r); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"severity": "high"`) {
		t.Errorf("severity must be serialized as a lowercase string, output:\n%s", buf.String())
	}
}

func TestRenderSuccess_HumanAndJSON(t *testing.T) {
	report := output.NewReport(nil)
	report.Status = "succeeded"

	var human bytes.Buffer
	if err := output.RenderHuman(&human, report); err != nil {
		t.Fatalf("RenderHuman: %v", err)
	}
	if !strings.Contains(human.String(), "SUCCESS rollout completed") {
		t.Fatalf("unexpected successful human output: %q", human.String())
	}

	var jsonOutput bytes.Buffer
	if err := output.RenderJSON(&jsonOutput, report); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(jsonOutput.String(), `"status": "succeeded"`) {
		t.Fatalf("unexpected successful JSON output: %s", jsonOutput.String())
	}
}

func TestRenderHuman_ContainsCauseAndRemediation(t *testing.T) {
	r := output.NewReport(sampleResults())

	var buf bytes.Buffer
	if err := output.RenderHuman(&buf, r); err != nil {
		t.Fatalf("RenderHuman: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"high-severity cause", "high-severity remediation", "SKIP", "ResourceQuota unavailable", "OK", "probe-sanity"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output does not contain %q:\n%s", want, out)
		}
	}
}

func TestRenderHuman_UndeterminedFinding_MarkedExplicitly(t *testing.T) {
	r := output.NewReport([]output.Group{
		{
			ID: "crashloop",
			Findings: []model.Finding{{
				CheckID:      "crashloop-undetermined",
				Severity:     model.SeverityHigh,
				Cause:        "unclear cause",
				Resource:     model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "app-1"},
				Remediation:  model.Remediation{Summary: "collect more context", ContextDependent: true},
				Undetermined: true,
			}},
		},
	})

	var buf bytes.Buffer
	if err := output.RenderHuman(&buf, r); err != nil {
		t.Fatalf("RenderHuman: %v", err)
	}
	if !strings.Contains(buf.String(), "undetermined") {
		t.Errorf("an Undetermined finding must be explicitly marked as undetermined, output:\n%s", buf.String())
	}
}
