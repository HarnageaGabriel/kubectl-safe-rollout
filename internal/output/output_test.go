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
					Cause:       "causa bassa",
					Resource:    model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: "default", Name: "a"},
					Remediation: model.Remediation{Summary: "rimedio basso"},
				},
				{
					CheckID:     "pdb-consistency",
					Severity:    model.SeverityHigh,
					Cause:       "causa alta",
					Resource:    model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: "default", Name: "b"},
					Remediation: model.Remediation{Summary: "rimedio alto", ContextDependent: true},
				},
			},
		},
		{
			ID:         "quota-headroom",
			Skipped:    true,
			SkipReason: "ResourceQuota non accessibile",
		},
		{
			ID:       "probe-sanity",
			Findings: nil,
		},
	}
}

func TestNewReport_OrdinaPerSeveritaDecrescente(t *testing.T) {
	r := output.NewReport(sampleResults())

	findings := r.Results[0].Findings
	if len(findings) != 2 {
		t.Fatalf("attesi 2 finding nel primo CheckReport, got %d", len(findings))
	}
	if findings[0].Severity != model.SeverityHigh || findings[1].Severity != model.SeverityLow {
		t.Errorf("ordine non per severita' decrescente: %v poi %v", findings[0].Severity, findings[1].Severity)
	}
}

func TestReport_MaxSeverity(t *testing.T) {
	r := output.NewReport(sampleResults())

	sev, ok := r.MaxSeverity()
	if !ok {
		t.Fatal("MaxSeverity() ok=false, atteso true: ci sono finding")
	}
	if sev != model.SeverityHigh {
		t.Errorf("MaxSeverity() = %v, atteso High", sev)
	}
}

func TestReport_MaxSeverity_NessunFinding(t *testing.T) {
	r := output.NewReport([]output.Group{{ID: "ok-check"}})

	if _, ok := r.MaxSeverity(); ok {
		t.Error("MaxSeverity() ok=true senza finding, atteso false")
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
		t.Fatalf("il JSON prodotto non e' decodificabile: %v\noutput: %s", err, buf.String())
	}
	if len(decoded.Results) != len(r.Results) {
		t.Errorf("Results decodificati = %d, attesi %d", len(decoded.Results), len(r.Results))
	}
}

func TestRenderJSON_SeveritaComeStringa(t *testing.T) {
	r := output.NewReport(sampleResults())

	var buf bytes.Buffer
	if err := output.RenderJSON(&buf, r); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"severity": "high"`) {
		t.Errorf("severity deve essere serializzata come stringa minuscola, output:\n%s", buf.String())
	}
}

func TestRenderSuccess_UmanoEJSON(t *testing.T) {
	report := output.NewReport(nil)
	report.Status = "succeeded"

	var human bytes.Buffer
	if err := output.RenderHuman(&human, report); err != nil {
		t.Fatalf("RenderHuman: %v", err)
	}
	if !strings.Contains(human.String(), "SUCCESS rollout completato") {
		t.Fatalf("output human successo inatteso: %q", human.String())
	}

	var jsonOutput bytes.Buffer
	if err := output.RenderJSON(&jsonOutput, report); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(jsonOutput.String(), `"status": "succeeded"`) {
		t.Fatalf("output JSON successo inatteso: %s", jsonOutput.String())
	}
}

func TestRenderHuman_ContieneCausaERimedio(t *testing.T) {
	r := output.NewReport(sampleResults())

	var buf bytes.Buffer
	if err := output.RenderHuman(&buf, r); err != nil {
		t.Fatalf("RenderHuman: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"causa alta", "rimedio alto", "SKIP", "ResourceQuota non accessibile", "OK", "probe-sanity"} {
		if !strings.Contains(out, want) {
			t.Errorf("output umano non contiene %q:\n%s", want, out)
		}
	}
}

func TestRenderHuman_FindingNonDeterminato_MarcatoEsplicitamente(t *testing.T) {
	r := output.NewReport([]output.Group{
		{
			ID: "crashloop",
			Findings: []model.Finding{{
				CheckID:      "crashloop-undetermined",
				Severity:     model.SeverityHigh,
				Cause:        "causa non chiara",
				Resource:     model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "app-1"},
				Remediation:  model.Remediation{Summary: "raccogli piu' contesto", ContextDependent: true},
				Undetermined: true,
			}},
		},
	})

	var buf bytes.Buffer
	if err := output.RenderHuman(&buf, r); err != nil {
		t.Fatalf("RenderHuman: %v", err)
	}
	if !strings.Contains(buf.String(), "non determinata") {
		t.Errorf("un finding Undetermined deve essere marcato esplicitamente come non determinato, output:\n%s", buf.String())
	}
}
