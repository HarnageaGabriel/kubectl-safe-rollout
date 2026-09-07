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

package check_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func daemonSetForPDBCheck(desired int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: testNamespace},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels()},
			},
		},
		Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: desired},
	}
}

func daemonSetPDB(name string, minAvailable, maxUnavailable *intstr.IntOrString, generation, observedGeneration int64, conditions ...metav1.Condition) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: generation},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector:       &metav1.LabelSelector{MatchLabels: podLabels()},
			MinAvailable:   minAvailable,
			MaxUnavailable: maxUnavailable,
		},
		Status: policyv1.PodDisruptionBudgetStatus{
			ObservedGeneration: observedGeneration,
			Conditions:         conditions,
		},
	}
}

func syncFailedCondition() metav1.Condition {
	return metav1.Condition{
		Type:   policyv1.DisruptionAllowedCondition,
		Status: metav1.ConditionFalse,
		Reason: policyv1.SyncFailedReason,
	}
}

func runPDBDaemonsetCheck(t *testing.T, w workload.Workload, objs ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objs...)
	target := check.Target{Namespace: testNamespace, Workload: w, Client: client}
	res, err := check.PDBDaemonsetScale{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

func TestPDBDaemonsetScale_NonDaemonSetTarget_NoFindingsNoAPICall(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "poddisruptionbudgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatal("must not list PodDisruptionBudgets for a non-DaemonSet target")
		return false, nil, nil
	})

	target := check.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d), Client: client}
	res, err := check.PDBDaemonsetScale{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("non-DaemonSet target: want empty, non-skipped result, got %+v", res)
	}
}

func TestPDBDaemonsetScale_DesiredNumberScheduledZero_NoFindingsNoAPICall(t *testing.T) {
	ds := daemonSetForPDBCheck(0)
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "poddisruptionbudgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatal("must not list PodDisruptionBudgets when desiredNumberScheduled is 0")
		return false, nil, nil
	})

	target := check.Target{Namespace: testNamespace, Workload: workload.FromDaemonSet(ds), Client: client}
	res, err := check.PDBDaemonsetScale{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("desiredNumberScheduled=0: want empty, non-skipped result, got %+v", res)
	}
}

func TestPDBDaemonsetScale_NoMatchingPDB_NoFindings(t *testing.T) {
	ds := daemonSetForPDBCheck(3)
	other := daemonSetPDB("unrelated", intstrPtr(intstr.FromInt(1)), nil, 1, 1)
	other.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "other"}}

	res := runPDBDaemonsetCheck(t, workload.FromDaemonSet(ds), ds, other)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("no matching PDB: want empty result, got %+v", res)
	}
}

func TestPDBDaemonsetScale_MinAvailableInteger_NoFindings(t *testing.T) {
	ds := daemonSetForPDBCheck(3)
	pdb := daemonSetPDB("safe", intstrPtr(intstr.FromInt(1)), nil, 1, 1)

	res := runPDBDaemonsetCheck(t, workload.FromDaemonSet(ds), ds, pdb)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("integer minAvailable never reaches the broken code path: want empty result, got %+v", res)
	}
}

func TestPDBDaemonsetScale_MaxUnavailable_ConditionObserved_HighFinding(t *testing.T) {
	ds := daemonSetForPDBCheck(3)
	pdb := daemonSetPDB("broken", nil, intstrPtr(intstr.FromInt(1)), 2, 2, syncFailedCondition())

	res := runPDBDaemonsetCheck(t, workload.FromDaemonSet(ds), ds, pdb)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: the failure is already observed on the live object", f.Severity)
	}
	if f.CheckID != check.PDBDaemonsetScaleCheckID {
		t.Errorf("checkID = %q, want %q", f.CheckID, check.PDBDaemonsetScaleCheckID)
	}
	if f.Resource.Kind != "PodDisruptionBudget" || f.Resource.Name != "broken" {
		t.Errorf("Resource must identify the PodDisruptionBudget, got %+v", f.Resource)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must be context-dependent")
	}
	if len(f.Remediation.Commands) != 1 || !strings.Contains(f.Remediation.Commands[0], "kubectl get pdb broken") {
		t.Errorf("remediation must include a read-only kubectl get pdb command, got %+v", f.Remediation.Commands)
	}
	if !strings.Contains(f.Remediation.Commands[0], "-o jsonpath") {
		t.Errorf("remediation command must be read-only (jsonpath inspection), got %q", f.Remediation.Commands[0])
	}
}

func TestPDBDaemonsetScale_MaxUnavailable_ConditionMissing_MediumFinding(t *testing.T) {
	ds := daemonSetForPDBCheck(3)
	pdb := daemonSetPDB("pending", nil, intstrPtr(intstr.FromString("20%")), 1, 1)

	res := runPDBDaemonsetCheck(t, workload.FromDaemonSet(ds), ds, pdb)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium: the mechanism guarantees the outcome but the controller has not confirmed it yet", f.Severity)
	}
}

func TestPDBDaemonsetScale_MinAvailablePercentage_StaleGeneration_StillHighFromCondition(t *testing.T) {
	ds := daemonSetForPDBCheck(3)
	// Generation ahead of observedGeneration must NOT downgrade this to
	// Medium: verified live on kind that the disruption controller's
	// failSafe path (the one that writes the SyncFailed condition in the
	// first place) never populates observedGeneration at all — it stays
	// permanently behind generation for a PDB stuck in this state. Gating
	// on it would make this check unable to ever report High for the exact
	// condition it exists to detect (see pdbDaemonsetSeverity's doc
	// comment). The condition itself, already present here, is the
	// verified ground truth.
	pdb := daemonSetPDB("stale", intstrPtr(intstr.FromString("50%")), nil, 3, 1, syncFailedCondition())

	res := runPDBDaemonsetCheck(t, workload.FromDaemonSet(ds), ds, pdb)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: a live SyncFailed condition is the verified fact, independent of observedGeneration", f.Severity)
	}
}

// TestPDBDaemonsetScale_ObservedGenerationNeverSet_StillHighFromCondition
// reproduces the literal shape observed live on kind (v1.37.0): the
// disruption controller's failSafe path never writes observedGeneration at
// all for a PDB stuck in SyncFailed, so it stays at its zero value (never
// "0 and 1" from a transient lag — permanently 0) while generation is 1
// from the single PDB create. A regression here means this check can never
// report High for the exact live-verified condition it exists to catch.
func TestPDBDaemonsetScale_ObservedGenerationNeverSet_StillHighFromCondition(t *testing.T) {
	ds := daemonSetForPDBCheck(3)
	pdb := daemonSetPDB("never-observed", nil, intstrPtr(intstr.FromInt32(1)), 1, 0, syncFailedCondition())

	res := runPDBDaemonsetCheck(t, workload.FromDaemonSet(ds), ds, pdb)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: observedGeneration=0 is the permanent, real-world value on this failure path, not a transient lag to distrust", f.Severity)
	}
}

func TestPDBDaemonsetScale_ListFailed_Skipped(t *testing.T) {
	ds := daemonSetForPDBCheck(3)
	client := fake.NewSimpleClientset(ds)
	client.PrependReactor("list", "poddisruptionbudgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading PodDisruptionBudgets")
	})

	target := check.Target{Namespace: testNamespace, Workload: workload.FromDaemonSet(ds), Client: client}
	res, err := check.PDBDaemonsetScale{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatal("want Skipped=true when the PDB list is not accessible")
	}
	if res.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
