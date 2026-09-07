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

// completeRolloutStatus returns a Deployment Status that makes
// deploymentWorkload.RolloutComplete() report true for the given replica
// count: ObservedGeneration caught up, UpdatedReplicas/Replicas/
// AvailableReplicas all at the desired count.
func completeRolloutStatus(replicas int32) appsv1.DeploymentStatus {
	return appsv1.DeploymentStatus{
		ObservedGeneration: 1,
		UpdatedReplicas:    replicas,
		Replicas:           replicas,
		AvailableReplicas:  replicas,
	}
}

func runPDBEvictionCheck(t *testing.T, w workload.Workload, objs ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objs...)
	target := check.Target{Namespace: testNamespace, Workload: w, Client: client}
	res, err := check.PDBEvictionBlocked{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

// pdbWithStatus builds a PodDisruptionBudget with both a Spec (selector,
// minAvailable/maxUnavailable) and a live Status, for the fine-grained
// preconditions pdb-eviction-blocked's Fact B checks.
func pdbWithStatus(name string, minAvailable, maxUnavailable *intstr.IntOrString, generation int64, status policyv1.PodDisruptionBudgetStatus) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: generation},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector:       &metav1.LabelSelector{MatchLabels: podLabels()},
			MinAvailable:   minAvailable,
			MaxUnavailable: maxUnavailable,
		},
		Status: status,
	}
}

// 1. Fact A: two PDBs matching the same pod -> exactly one High finding
// naming both.
func TestPDBEvictionBlocked_TwoMatchingPDBs_HighFinding(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	d.Status = completeRolloutStatus(3)

	first := pdb("first", podLabels(), nil, intstrPtr(intstr.FromInt(1)))
	first.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 0, DisruptionsAllowed: 1}
	second := pdb("second", podLabels(), intstrPtr(intstr.FromInt(1)), nil)
	second.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 0, DisruptionsAllowed: 2}

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, first, second)
	if res.Skipped {
		t.Fatalf("must not skip, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.CheckID != check.PDBEvictionBlockedCheckID {
		t.Errorf("checkID = %q, want %q", f.CheckID, check.PDBEvictionBlockedCheckID)
	}
	if !strings.Contains(f.Cause, "first") || !strings.Contains(f.Cause, "second") {
		t.Errorf("Cause must name both PodDisruptionBudgets, got %q", f.Cause)
	}
	foundBoth := false
	for _, e := range f.Evidence {
		if strings.Contains(e, "first") && strings.Contains(e, "second") {
			foundBoth = true
		}
	}
	if !foundBoth {
		t.Errorf("Evidence must name both PodDisruptionBudgets, got %+v", f.Evidence)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must be context-dependent")
	}
	if len(f.Remediation.Commands) != 1 {
		t.Fatalf("want exactly 1 remediation command, got %+v", f.Remediation.Commands)
	}
	if !strings.Contains(f.Remediation.Commands[0], "first") || !strings.Contains(f.Remediation.Commands[0], "second") {
		t.Errorf("remediation command must name both PodDisruptionBudgets, got %q", f.Remediation.Commands[0])
	}
}

// 2. Fact B: single PDB, spec predicts headroom, live status reports zero
// for a reason the spec cannot see, rollout complete -> exactly one Medium.
func TestPDBEvictionBlocked_SinglePDB_LiveStatusZero_RolloutComplete_MediumFinding(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	d.Generation = 1
	d.Status = completeRolloutStatus(3)

	p := pdbWithStatus("checkout-pdb", nil, intstrPtr(intstr.FromInt(1)), 1, policyv1.PodDisruptionBudgetStatus{
		ObservedGeneration: 1,
		DisruptionsAllowed: 0,
		CurrentHealthy:     2,
		DesiredHealthy:     2,
		ExpectedPods:       3,
	})

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, p)
	if res.Skipped {
		t.Fatalf("must not skip, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium", f.Severity)
	}
	if f.Resource.Kind != "PodDisruptionBudget" || f.Resource.Name != "checkout-pdb" {
		t.Errorf("Resource must identify the PodDisruptionBudget, got %+v", f.Resource)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must be context-dependent")
	}
	if len(f.Remediation.Commands) != 1 || !strings.Contains(f.Remediation.Commands[0], "kubectl get pdb checkout-pdb") || !strings.Contains(f.Remediation.Commands[0], "-o jsonpath='{.status}'") {
		t.Errorf("remediation must include a read-only status inspection command, got %+v", f.Remediation.Commands)
	}
}

// 3. No problem: disruptionsAllowed=1 in steady state -> zero findings.
func TestPDBEvictionBlocked_SinglePDB_HealthyDisruptionsAllowed_NoFindings(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	d.Generation = 1
	d.Status = completeRolloutStatus(3)

	p := pdbWithStatus("checkout-pdb", nil, intstrPtr(intstr.FromInt(1)), 1, policyv1.PodDisruptionBudgetStatus{
		ObservedGeneration: 1,
		DisruptionsAllowed: 1,
	})

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, p)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("want empty, non-skipped result, got %+v", res)
	}
}

// 4. Anti-regression, the most important negative case: disruptionsAllowed=0
// but the rollout is NOT complete -> zero findings. Not-ready pods during a
// rollout are normal; removing this gate must break this test loudly.
func TestPDBEvictionBlocked_DisruptionsAllowedZero_RolloutNotComplete_NoFindings(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	// Deliberately no Status set: RolloutComplete() is false by construction
	// (UpdatedReplicas 0 < desired 3).

	p := pdbWithStatus("checkout-pdb", nil, intstrPtr(intstr.FromInt(1)), 0, policyv1.PodDisruptionBudgetStatus{
		ObservedGeneration: 0,
		DisruptionsAllowed: 0,
	})

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, p)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("rollout not complete must suppress this finding entirely: want empty result, got %+v", res)
	}
}

// 5. No problem: disruptionsAllowed=0 but disruptedPods is non-empty (a
// recent eviction still inside DeletionTimeout) -> zero findings.
func TestPDBEvictionBlocked_DisruptedPodsNonEmpty_NoFindings(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	d.Generation = 1
	d.Status = completeRolloutStatus(3)

	p := pdbWithStatus("checkout-pdb", nil, intstrPtr(intstr.FromInt(1)), 1, policyv1.PodDisruptionBudgetStatus{
		ObservedGeneration: 1,
		DisruptionsAllowed: 0,
		DisruptedPods:      map[string]metav1.Time{"checkout-abc123": {}},
	})

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, p)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a recent eviction still inside DeletionTimeout must not be flagged: want empty result, got %+v", res)
	}
}

// 6. No problem: observedGeneration < generation -> zero findings (stale
// status, not evidence of anything).
func TestPDBEvictionBlocked_StaleObservedGeneration_NoFindings(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	d.Generation = 1
	d.Status = completeRolloutStatus(3)

	p := pdbWithStatus("checkout-pdb", nil, intstrPtr(intstr.FromInt(1)), 2, policyv1.PodDisruptionBudgetStatus{
		ObservedGeneration: 1,
		DisruptionsAllowed: 0,
	})

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, p)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a stale status must not be flagged: want empty result, got %+v", res)
	}
}

// 7. Deduplication with pdb-consistency: minAvailable == replicas, so the
// Spec arithmetic itself predicts zero headroom (pdb-consistency already
// reports this at High). This check must stay silent even though the live
// status also reports zero and the rollout is complete.
func TestPDBEvictionBlocked_SpecPredictsZeroHeadroom_NoFindingsFromThisCheck(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	d.Generation = 1
	d.Status = completeRolloutStatus(3)

	p := pdbWithStatus("checkout-pdb", intstrPtr(intstr.FromInt(3)), nil, 1, policyv1.PodDisruptionBudgetStatus{
		ObservedGeneration: 1,
		DisruptionsAllowed: 0,
	})

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, p)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("spec already predicts zero headroom, pdb-consistency owns that finding: want empty result from pdb-eviction-blocked, got %+v", res)
	}
}

// 8. Deduplication with pdb-daemonset-scale: DaemonSet + maxUnavailable PDB
// stuck in the real failSafe shape observed on kind (generation ahead of the
// permanently-unset observedGeneration, DisruptionAllowed=False/SyncFailed).
// pdb-eviction-blocked's ObservedGeneration==Generation precondition must
// keep this check silent; pdb-daemonset-scale owns this scenario.
func TestPDBEvictionBlocked_DaemonSetSyncFailedPDB_NoFindingsFromThisCheck(t *testing.T) {
	ds := daemonSetForPDBCheck(3)
	pdbObj := daemonSetPDB("broken", nil, intstrPtr(intstr.FromInt(1)), 1, 0, syncFailedCondition())

	res := runPDBEvictionCheck(t, workload.FromDaemonSet(ds), ds, pdbObj)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a DaemonSet PDB stuck in SyncFailed is owned by pdb-daemonset-scale: want empty result from pdb-eviction-blocked, got %+v", res)
	}
}

// 9. Degradation: PDB list denied -> Skipped with a reason.
func TestPDBEvictionBlocked_ListFailed_Skipped(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	client := fake.NewSimpleClientset(d)
	client.PrependReactor("list", "poddisruptionbudgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading PodDisruptionBudgets")
	})

	target := check.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d), Client: client}
	res, err := check.PDBEvictionBlocked{}.Run(context.Background(), target)
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

// 10a. Nothing to evaluate: desired count 0 -> zero findings, no API call.
func TestPDBEvictionBlocked_ZeroReplicas_NoFindingsNoAPICall(t *testing.T) {
	d := deployment(0, rollingUpdateStrategy(nil))
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "poddisruptionbudgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatal("must not list PodDisruptionBudgets when replicas is 0")
		return false, nil, nil
	})

	target := check.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d), Client: client}
	res, err := check.PDBEvictionBlocked{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("replicas=0: want empty, non-skipped result, got %+v", res)
	}
}

// 10b. Nothing to evaluate: a PDB selecting a different workload entirely
// -> zero findings.
func TestPDBEvictionBlocked_PDBSelectsDifferentWorkload_NoFindings(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	d.Generation = 1
	d.Status = completeRolloutStatus(3)

	other := pdb("unrelated", map[string]string{"app": "other"}, intstrPtr(intstr.FromInt(1)), nil)
	other.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, DisruptionsAllowed: 0}

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, other)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("PDB selects a different workload: want empty result, got %+v", res)
	}
}

// 11. An explicit empty ({}) selector must be treated as matching every pod
// in the namespace, same reasoning already applied to pdb-consistency and
// pdb-daemonset-scale.
func TestPDBEvictionBlocked_ExplicitEmptySelector_MatchesAllPods(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	d.Generation = 1
	d.Status = completeRolloutStatus(3)

	p := pdbWithStatus("catch-all", nil, intstrPtr(intstr.FromInt(1)), 1, policyv1.PodDisruptionBudgetStatus{
		ObservedGeneration: 1,
		DisruptionsAllowed: 0,
	})
	p.Spec.Selector = &metav1.LabelSelector{}

	res := runPDBEvictionCheck(t, workload.FromDeployment(d), d, p)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("an explicit empty selector must be treated as matching this workload's pods: want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium", res.Findings[0].Severity)
	}
}
