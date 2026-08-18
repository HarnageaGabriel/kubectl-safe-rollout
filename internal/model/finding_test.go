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

package model_test

import (
	"encoding/json"
	"testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

func TestSeverity_NumericOrdering(t *testing.T) {
	// Exit code gating in cmd/kubectl-safe_rollout compares severities
	// with >: if value order changes, that comparison silently breaks.
	// This test locks down the contract.
	if model.SeverityLow >= model.SeverityMedium || model.SeverityMedium >= model.SeverityHigh {
		t.Fatalf("severity order violated: low=%d medium=%d high=%d",
			model.SeverityLow, model.SeverityMedium, model.SeverityHigh)
	}
}

func TestSeverity_JSONRoundTrip(t *testing.T) {
	for _, sev := range []model.Severity{model.SeverityLow, model.SeverityMedium, model.SeverityHigh} {
		data, err := json.Marshal(sev)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", sev, err)
		}
		var got model.Severity
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if got != sev {
			t.Errorf("round-trip: got %v, want %v (json: %s)", got, sev, data)
		}
	}
}

func TestSeverity_UnmarshalUnknownValue(t *testing.T) {
	var s model.Severity
	if err := json.Unmarshal([]byte(`"critical"`), &s); err == nil {
		t.Fatal("expected error for unknown severity, got nil")
	}
}

func TestResourceRef_String(t *testing.T) {
	r := model.ResourceRef{Kind: "Deployment", Namespace: "default", Name: "api"}
	if got, want := r.String(), "Deployment/api"; got != want {
		t.Errorf("ResourceRef.String() = %q, want %q", got, want)
	}
}
