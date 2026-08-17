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

func TestSeverity_OrdinamentoNumerico(t *testing.T) {
	// Il gating dell'exit code in cmd/kubectl-safe_rollout confronta le
	// severita' con >: se l'ordine dei valori cambia, quel confronto si
	// rompe silenziosamente. Questo test fissa il contratto.
	if model.SeverityLow >= model.SeverityMedium || model.SeverityMedium >= model.SeverityHigh {
		t.Fatalf("ordine severita' violato: low=%d medium=%d high=%d",
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
			t.Errorf("round-trip: got %v, atteso %v (json: %s)", got, sev, data)
		}
	}
}

func TestSeverity_UnmarshalValoreSconosciuto(t *testing.T) {
	var s model.Severity
	if err := json.Unmarshal([]byte(`"critical"`), &s); err == nil {
		t.Fatal("atteso errore per severity sconosciuta, got nil")
	}
}

func TestResourceRef_String(t *testing.T) {
	r := model.ResourceRef{Kind: "Deployment", Namespace: "default", Name: "api"}
	if got, want := r.String(), "Deployment/api"; got != want {
		t.Errorf("ResourceRef.String() = %q, atteso %q", got, want)
	}
}
