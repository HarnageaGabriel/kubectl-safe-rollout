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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func selectorOverlapDeployment(uid types.UID, name string, selectorLabels, podTemplateLabels map[string]string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: uid},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podTemplateLabels},
			},
		},
	}
}

func selectorOverlapStatefulSet(uid types.UID, name string, selectorLabels, podTemplateLabels map[string]string, replicas int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: uid},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podTemplateLabels},
			},
		},
	}
}

func runSelectorOverlapCheck(t *testing.T, target workload.Workload, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.SelectorOverlap{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  target,
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestSelectorOverlap_IdenticalMatchLabels_MediumFinding(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 3)
	other := selectorOverlapDeployment("other-uid", "checkout-orphan-adopter", targetLabels, targetLabels, 2)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target, other)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want exactly one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.SelectorOverlapCheckID || f.Severity != model.SeverityMedium {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want Medium", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "Deployment/checkout") || !strings.Contains(f.Cause, "Deployment/checkout-orphan-adopter") {
		t.Errorf("cause must name both workloads, got %q", f.Cause)
	}
	if f.Resource.Kind != "Deployment" || f.Resource.Name != "checkout" {
		t.Errorf("Resource must be the target workload, not the colliding one: got %+v", f.Resource)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must be context-dependent: which workload is misconfigured is a judgment call")
	}
	if len(f.Remediation.Commands) != 1 {
		t.Errorf("want exactly one read-only inspection command, got %+v", f.Remediation.Commands)
	}
}

func TestSelectorOverlap_AsymmetricTargetToOtherOnly_FlaggedWithSingleDirection(t *testing.T) {
	target := selectorOverlapDeployment("target-uid", "checkout", map[string]string{"app": "x"}, map[string]string{"app": "x", "role": "a"}, 1)
	other := selectorOverlapDeployment("other-uid", "other", map[string]string{"app": "x", "role": "b"}, map[string]string{"app": "x", "role": "b"}, 1)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target, other)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want exactly one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if !contains(f.Evidence, "selfSelectorMatchesOtherLabels") {
		t.Errorf("evidence must name the target->other direction, got %+v", f.Evidence)
	}
	if contains(f.Evidence, "otherSelectorMatchesSelfLabels") {
		t.Errorf("evidence must NOT name the other->target direction, an equality check would wrongly claim it: got %+v", f.Evidence)
	}
}

func TestSelectorOverlap_AsymmetricOtherToTargetOnly_FlaggedWithSingleDirection(t *testing.T) {
	// Mirror of the previous test: now it is the other workload's selector
	// that matches the target's pod labels, not the reverse.
	target := selectorOverlapDeployment("target-uid", "checkout", map[string]string{"app": "x", "role": "b"}, map[string]string{"app": "x", "role": "b"}, 1)
	other := selectorOverlapDeployment("other-uid", "other", map[string]string{"app": "x"}, map[string]string{"app": "x", "role": "a"}, 1)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target, other)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want exactly one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if !contains(f.Evidence, "otherSelectorMatchesSelfLabels") {
		t.Errorf("evidence must name the other->target direction, got %+v", f.Evidence)
	}
	if contains(f.Evidence, "selfSelectorMatchesOtherLabels") {
		t.Errorf("evidence must NOT name the target->other direction: got %+v", f.Evidence)
	}
}

func TestSelectorOverlap_IncidentalSharedLabelDisjointRequiredTerms_NoFindings(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout", "team": "payments"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)
	otherLabels := map[string]string{"app": "cart", "team": "payments"}
	other := selectorOverlapDeployment("other-uid", "cart", otherLabels, otherLabels, 1)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target, other)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("selectors sharing only one incidental label with an otherwise disjoint required term must not be flagged, got %+v", result)
	}
}

func TestSelectorOverlap_TargetInOwnListResult_ExcludedByUID(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("target must be excluded from its own candidate list via UID, got %+v", result)
	}
}

func TestSelectorOverlap_CrossKindStatefulSetCollision_FlaggedNamingOtherKind(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)
	other := selectorOverlapStatefulSet("other-uid", "checkout-db", targetLabels, targetLabels, 1)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target, other)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want exactly one non-skipped finding for a cross-kind collision, got %+v", result)
	}
	f := result.Findings[0]
	if !contains(f.Evidence, "otherKind=StatefulSet") {
		t.Errorf("evidence must name the other workload's kind, got %+v", f.Evidence)
	}
}

func TestSelectorOverlap_OtherWorkloadZeroReplicas_StillFlaggedMediumWithZeroInEvidence(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)
	other := selectorOverlapDeployment("other-uid", "other", targetLabels, targetLabels, 0)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target, other)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want exactly one non-skipped finding even when the other workload has zero replicas, got %+v", result)
	}
	f := result.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity must remain Medium regardless of the other workload's replica count, got %v", f.Severity)
	}
	if !contains(f.Evidence, "otherReplicas=0") {
		t.Errorf("evidence must record the other workload's replica count as 0, got %+v", f.Evidence)
	}
}

func TestSelectorOverlap_OtherWorkloadUnparseableSelector_SkippedSilentlyForThatCandidate(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)

	// An invalid MatchExpressions operator makes PodSelector() return an
	// error for this one candidate: it must be skipped silently, not abort
	// evaluation of the rest.
	broken := selectorOverlapDeployment("broken-uid", "broken", targetLabels, targetLabels, 1)
	broken.Spec.Selector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: "NotAnOperator", Values: []string{"checkout"}}},
	}
	// A second, genuinely colliding candidate proves the loop kept going
	// after skipping the broken one.
	colliding := selectorOverlapDeployment("colliding-uid", "colliding", targetLabels, targetLabels, 1)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target, broken, colliding)
	if result.Skipped {
		t.Fatalf("a single malformed candidate must not cause the whole check to skip, got %+v", result)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("want exactly one finding (from the colliding candidate only), got %+v", result.Findings)
	}
	if result.Findings[0].Resource.Name != "checkout" {
		t.Errorf("unexpected finding resource: %+v", result.Findings[0])
	}
	for _, f := range result.Findings {
		if strings.Contains(f.Cause, "broken") {
			t.Errorf("the malformed candidate must never appear in a finding, got %q", f.Cause)
		}
	}
}

func TestSelectorOverlap_NoOtherWorkloadsInNamespace_NoFindingsNotSkipped(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target)
	if result.Skipped {
		t.Fatal("an empty candidate set is a checked, clean result, not a Skip")
	}
	if len(result.Findings) != 0 {
		t.Fatalf("want zero findings, got %+v", result.Findings)
	}
}

func TestSelectorOverlap_DeploymentListDenied_Skipped(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)
	client := fake.NewSimpleClientset(target)
	client.PrependReactor("list", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing Deployments")
	})

	result, err := check.SelectorOverlap{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(target),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the Deployment list is not accessible")
	}
	if !strings.Contains(result.SkipReason, "deployment") {
		t.Errorf("skip reason must name \"deployment\", got %q", result.SkipReason)
	}
}

func TestSelectorOverlap_StatefulSetListDenied_SkippedNamingStatefulSetSpecifically(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)
	client := fake.NewSimpleClientset(target)
	client.PrependReactor("list", "statefulsets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing StatefulSets")
	})

	result, err := check.SelectorOverlap{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(target),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the StatefulSet list is not accessible, even though the Deployment list succeeded")
	}
	if !strings.Contains(result.SkipReason, "statefulset") {
		t.Errorf("skip reason must specifically name \"statefulset\", not a generic reused message, got %q", result.SkipReason)
	}
	if strings.Contains(result.SkipReason, "deployment list") {
		t.Errorf("skip reason must not be the Deployment-list message reused for a different failure, got %q", result.SkipReason)
	}
}

func TestSelectorOverlap_ThreeCollidingWorkloads_DeterministicOrder(t *testing.T) {
	targetLabels := map[string]string{"app": "checkout"}
	target := selectorOverlapDeployment("target-uid", "checkout", targetLabels, targetLabels, 1)
	depB := selectorOverlapDeployment("depb-uid", "zzz-deploy", targetLabels, targetLabels, 1)
	depA := selectorOverlapDeployment("depa-uid", "aaa-deploy", targetLabels, targetLabels, 1)
	sts := selectorOverlapStatefulSet("sts-uid", "mid-sts", targetLabels, targetLabels, 1)

	result := runSelectorOverlapCheck(t, workload.FromDeployment(target), target, depB, depA, sts)
	if result.Skipped || len(result.Findings) != 3 {
		t.Fatalf("want exactly three findings, got %+v", result)
	}
	wantOrder := []string{"Deployment/aaa-deploy", "Deployment/zzz-deploy", "StatefulSet/mid-sts"}
	for i, want := range wantOrder {
		if !strings.Contains(result.Findings[i].Cause, want) {
			t.Errorf("finding %d: want cause naming %q, got %q", i, want, result.Findings[i].Cause)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
