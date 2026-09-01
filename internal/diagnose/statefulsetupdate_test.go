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
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func statefulSetUpdateTarget(s *appsv1.StatefulSet) diagnose.Target {
	return diagnose.Target{Namespace: testNamespace, Workload: workload.FromStatefulSet(s)}
}

func TestStatefulSetUpdate_OnDelete_PendingUpdate_High(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       int32Ptr(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision:  "app-7f8c9",
			CurrentRevision: "app-5a1b2",
		},
	}
	target := statefulSetUpdateTarget(s)

	res, err := diagnose.StatefulSetUpdate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseStatefulSetUpdateOnDelete) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseStatefulSetUpdateOnDelete, res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("Severity = %v, expected High", f.Severity)
	}
	if f.Undetermined {
		t.Error("OnDelete with mismatched revisions is a structured fact, not ambiguous: must not be Undetermined")
	}
	if !strings.Contains(f.Cause, "app-7f8c9") || !strings.Contains(f.Cause, "app-5a1b2") {
		t.Errorf("Cause must name both the pending and current revisions, got %q", f.Cause)
	}
	if len(f.Remediation.Commands) != 0 {
		t.Error("deleting pods is a real state change, not a read-only command: Commands must stay empty")
	}
	if !f.Remediation.ContextDependent {
		t.Error("OnDelete is frequently deliberate: Remediation must be ContextDependent")
	}
	if !strings.Contains(f.Remediation.Summary, "app-7f8c9") {
		t.Errorf("Remediation.Summary must name the pending revision the operator must move pods to, got %q", f.Remediation.Summary)
	}
}

func TestStatefulSetUpdate_OnDelete_RevisionsMatch_NoFinding(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       int32Ptr(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision:  "app-5a1b2",
			CurrentRevision: "app-5a1b2",
		},
	}
	target := statefulSetUpdateTarget(s)

	res, err := diagnose.StatefulSetUpdate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("no pending update: must produce no findings, got %+v", res.Findings)
	}
}

func TestStatefulSetUpdate_PartitionAtOrAboveReplicas_High(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: int32Ptr(3),
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision:  "app-7f8c9",
			CurrentRevision: "app-5a1b2",
		},
	}
	target := statefulSetUpdateTarget(s)

	res, err := diagnose.StatefulSetUpdate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseStatefulSetPartitionBlocked) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseStatefulSetPartitionBlocked, res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("Severity = %v, expected High", f.Severity)
	}
	if !f.Remediation.ContextDependent {
		t.Error("lowering partition is a spec edit only the operator can authorize: must be ContextDependent")
	}
	if len(f.Remediation.Commands) != 1 || !strings.Contains(f.Remediation.Commands[0], "partition") {
		t.Fatalf("expected exactly 1 read-only command inspecting the current partition, got %+v", f.Remediation.Commands)
	}
}

// The defining false-positive guard for this Diagnoser: 0 < partition <
// replicas is the standard canary idiom (only pods with an ordinal >=
// partition ever update), deliberate and healthy, not a stuck rollout. A
// naive "partition is set" check would wrongly flag every canary rollout.
func TestStatefulSetUpdate_CanaryPartition_NoFinding(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(5),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: int32Ptr(3),
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision:  "app-7f8c9",
			CurrentRevision: "app-5a1b2",
		},
	}
	target := statefulSetUpdateTarget(s)

	res, err := diagnose.StatefulSetUpdate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("0 < partition < replicas is the canary idiom, a deliberate healthy configuration: must produce no findings, got %+v", res.Findings)
	}
}

func TestStatefulSetUpdate_RollingUpdateNoPartition_NoFinding(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       int32Ptr(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{
			UpdateRevision:  "app-7f8c9",
			CurrentRevision: "app-5a1b2",
		},
	}
	target := statefulSetUpdateTarget(s)

	res, err := diagnose.StatefulSetUpdate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("the common case, no partition set: must produce no findings, got %+v", res.Findings)
	}
}

func TestStatefulSetUpdate_Deployment_NoFinding(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(3)},
	}
	target := diagnose.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d)}

	res, err := diagnose.StatefulSetUpdate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("this Diagnoser is StatefulSet-specific: must produce no findings for Deployment, got %+v", res.Findings)
	}
}
