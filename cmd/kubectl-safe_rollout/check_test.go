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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/kube"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/output"
)

func TestCheckOutputFormat_AcceptsHumanAndJSONOnly(t *testing.T) {
	for _, format := range []string{"human", "json"} {
		t.Run("accepts "+format, func(t *testing.T) {
			if err := validateOutputFormat(format); err != nil {
				t.Fatalf("validateOutputFormat(%q) returned error: %v", format, err)
			}
		})
	}

	for _, format := range []string{"", "yaml", "Human", "JSON", " human"} {
		t.Run("rejects "+fmt.Sprintf("%q", format), func(t *testing.T) {
			cmd := newCheckCommand(genericclioptions.NewConfigFlags(true))
			cmd.SetArgs([]string{"deployment/api", "--output", format})

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("--output %q succeeded, expected an error", format)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("invalid --output: %q", format)) {
				t.Fatalf("error %q does not name invalid output value %q", err, format)
			}
		})
	}
}

func TestRenderCheckReport_HighSeverityReturnsExitSentinel(t *testing.T) {
	report := reportWithFindings(model.Finding{
		CheckID:     "pdb-consistency",
		Severity:    model.SeverityHigh,
		Cause:       "PDB permits no voluntary disruptions",
		Remediation: model.Remediation{Summary: "review the PDB", ContextDependent: true},
		Resource:    model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: "default", Name: "api"},
	})

	err := renderCheckReport(&bytes.Buffer{}, report, "human")
	if !errors.Is(err, errHighSeverity) {
		t.Fatalf("renderCheckReport() error = %v, expected errHighSeverity", err)
	}
}

func TestRenderCheckReport_MediumAndLowSeveritiesDoNotFail(t *testing.T) {
	report := reportWithFindings(
		model.Finding{
			CheckID:     "quota-headroom",
			Severity:    model.SeverityMedium,
			Cause:       "limited quota headroom",
			Remediation: model.Remediation{Summary: "review quota", ContextDependent: true},
			Resource:    model.ResourceRef{Kind: "ResourceQuota", Namespace: "default", Name: "compute"},
		},
		model.Finding{
			CheckID:     "probe-sanity",
			Severity:    model.SeverityLow,
			Cause:       "readiness probe absent",
			Remediation: model.Remediation{Summary: "add a readiness probe", ContextDependent: true},
			Resource:    model.ResourceRef{Kind: "Deployment", Namespace: "default", Name: "api"},
		},
	)

	if err := renderCheckReport(&bytes.Buffer{}, report, "human"); err != nil {
		t.Fatalf("renderCheckReport() returned error for medium/low findings: %v", err)
	}
}

func TestRenderCheckReport_JSONPreservesDocumentedFieldNames(t *testing.T) {
	report := reportWithFindings(model.Finding{
		CheckID:      "crashloop-undetermined",
		Severity:     model.SeverityMedium,
		Cause:        "restart cause is undetermined",
		Evidence:     []string{"container restarted three times"},
		Remediation:  model.Remediation{Summary: "inspect previous logs", Commands: []string{"kubectl logs api --previous"}},
		Resource:     model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "api-abc123"},
		Undetermined: true,
	})

	var rendered bytes.Buffer
	if err := renderCheckReport(&rendered, report, "json"); err != nil {
		t.Fatalf("renderCheckReport() returned error: %v", err)
	}

	var decoded output.Report
	if err := json.Unmarshal(rendered.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON output does not match output.Report: %v\n%s", err, rendered.String())
	}
	if len(decoded.Results) != 1 || len(decoded.Results[0].Findings) != 1 {
		t.Fatalf("decoded report has unexpected result shape: %+v", decoded)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(rendered.Bytes(), &document); err != nil {
		t.Fatalf("JSON output cannot be unmarshaled: %v\n%s", err, rendered.String())
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(document["results"], &results); err != nil || len(results) != 1 {
		t.Fatalf("results shape is invalid: error=%v count=%d", err, len(results))
	}
	if _, exists := results[0]["checkId"]; !exists {
		t.Fatal("result is missing stable field name checkId")
	}

	var findings []map[string]json.RawMessage
	if err := json.Unmarshal(results[0]["findings"], &findings); err != nil || len(findings) != 1 {
		t.Fatalf("findings shape is invalid: error=%v count=%d", err, len(findings))
	}
	for _, field := range []string{"checkId", "severity", "cause", "evidence", "remediation", "resource", "undetermined"} {
		if _, exists := findings[0][field]; !exists {
			t.Errorf("finding is missing stable field name %s", field)
		}
	}

	var remediation map[string]json.RawMessage
	if err := json.Unmarshal(findings[0]["remediation"], &remediation); err != nil {
		t.Fatalf("remediation shape is invalid: %v", err)
	}
	for _, field := range []string{"summary", "commands", "contextDependent"} {
		if _, exists := remediation[field]; !exists {
			t.Errorf("remediation is missing stable field name %s", field)
		}
	}

	var resource map[string]json.RawMessage
	if err := json.Unmarshal(findings[0]["resource"], &resource); err != nil {
		t.Fatalf("resource shape is invalid: %v", err)
	}
	for _, field := range []string{"kind", "namespace", "name"} {
		if _, exists := resource[field]; !exists {
			t.Errorf("resource is missing stable field name %s", field)
		}
	}
}

func TestResolveWorkload_InvalidReferencesReturnClearErrors(t *testing.T) {
	client := fake.NewSimpleClientset()

	for _, ref := range []string{"deployment", "deployment/", "/name", "foo/bar/baz"} {
		t.Run(ref, func(t *testing.T) {
			_, err := kube.ResolveWorkload(context.Background(), client, "default", ref)
			if err == nil {
				t.Fatalf("ResolveWorkload(%q) succeeded, expected an error", ref)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("invalid reference %q", ref)) ||
				!strings.Contains(err.Error(), "expected <kind>/<name>") {
				t.Fatalf("ResolveWorkload(%q) returned unclear error: %v", ref, err)
			}
		})
	}
}

func reportWithFindings(findings ...model.Finding) output.Report {
	return output.NewReport([]output.Group{{
		ID:       "contract-test",
		Findings: findings,
	}})
}
