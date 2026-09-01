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

package diagnose_test

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// Reproduces what running `watch` against a real paused Deployment on kind
// showed: no pod-level symptom exists to classify, since a paused controller
// never creates or updates anything, and progressDeadlineSeconds itself never
// fires while paused. Without this Diagnoser the caller has no way to explain
// why watch never converges.
func TestPaused_SpecPausedTrue_HighFinding(t *testing.T) {
	seconds := int32(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec:       appsv1.DeploymentSpec{Paused: true, ProgressDeadlineSeconds: &seconds},
	}
	target := diagnose.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d)}

	res, err := diagnose.Paused{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseRolloutPaused) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseRolloutPaused, res.Findings)
	}
	if res.Findings[0].Undetermined {
		t.Error("spec.paused is a boolean structured signal, not ambiguous: must not be Undetermined")
	}
	if len(res.Findings[0].Remediation.Commands) != 0 {
		t.Error("resuming is a real state change, not a read-only command: Commands must stay empty")
	}
}

// StatefulSet has no spec.paused field at all: watch must say so explicitly
// (Skipped=true) rather than silently reporting no Finding, which would be
// indistinguishable from "evaluated and found not paused".
func TestPaused_StatefulSet_Skipped(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace},
	}
	target := diagnose.Target{Namespace: testNamespace, Workload: workload.FromStatefulSet(s)}

	res, err := diagnose.Paused{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if !res.Skipped {
		t.Fatal("StatefulSet has no pause mechanism: this must be Skipped, not silently empty")
	}
	if !strings.Contains(res.SkipReason, "StatefulSet") {
		t.Errorf("SkipReason must name the kind, got %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a Skipped result must carry no Findings, got %+v", res.Findings)
	}
}

func TestPaused_NotPaused_NoFinding(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
	}
	target := diagnose.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d)}

	res, err := diagnose.Paused{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected no findings for a non-paused workload, got %+v", res.Findings)
	}
}
