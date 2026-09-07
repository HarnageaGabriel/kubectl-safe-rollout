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

//go:build e2e

// The default kind cluster this project uses (`make kind-up`) is
// single-node: desiredNumberScheduled for any DaemonSet on it is always 1.
// Multi-node DaemonSet rollout scenarios (e.g. maxUnavailable/maxSurge
// behavior actually spanning several nodes) are therefore not verifiable
// on this cluster and are deliberately out of scope for this file, the
// same limitation already documented at the top of scheduling_test.go for
// the same reason.
package e2e_test

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// deployDaemonSet creates a single-container DaemonSet with matching
// selector labels in Spec.Selector and Template, mirroring deployWorkload's
// (Deployment) and deployStatefulSet's shape. DaemonSet has no
// spec.replicas field at all — the desired count is entirely
// controller-computed from node eligibility — so unlike its two siblings
// this helper takes no replica count parameter. DaemonSet also owns no
// companion cluster-scoped or cross-namespace object (no headless Service,
// unlike StatefulSet), so no cleanup beyond the ephemeral namespace
// deletion newE2ENamespace already registers is needed.
func deployDaemonSet(t *testing.T, client kubernetes.Interface, namespace, name string, podSpec corev1.PodSpec, mutate func(*appsv1.DaemonSet)) *appsv1.DaemonSet {
	t.Helper()
	labels := map[string]string{"app": name}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
	if mutate != nil {
		mutate(ds)
	}
	created, err := client.AppsV1().DaemonSets(namespace).Create(context.Background(), ds, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating DaemonSet %s/%s: %v", namespace, name, err)
	}
	return created
}

// watchDaemonSetAndExpectCause mirrors watchStatefulSetAndExpectCause, for
// a DaemonSet instead of a StatefulSet.
func watchDaemonSetAndExpectCause(t *testing.T, client kubernetes.Interface, namespace string, d *appsv1.DaemonSet, causeID diagnose.CauseID, timeout time.Duration) diagnose.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome, err := diagnose.Watch(ctx, diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  workload.FromDaemonSet(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Watch returned an unexpected error: %v", err)
	}
	if outcome.Succeeded {
		t.Fatalf("Watch reported success, expected failure %q", causeID)
	}
	for _, res := range outcome.Results {
		for _, f := range res.Findings {
			if f.CheckID == string(causeID) {
				return outcome
			}
		}
	}
	t.Fatalf("cause %q not found among Results: %+v", causeID, outcome.Results)
	return diagnose.Outcome{}
}

// watchDaemonSetAndExpectSuccess mirrors watchStatefulSetAndExpectSuccess,
// for a DaemonSet instead of a StatefulSet.
func watchDaemonSetAndExpectSuccess(t *testing.T, client kubernetes.Interface, namespace string, d *appsv1.DaemonSet, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome, err := diagnose.Watch(ctx, diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  workload.FromDaemonSet(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Watch returned an unexpected error: %v", err)
	}
	findings := diagnose.AllFindings(outcome.Results)
	if len(findings) != 0 {
		t.Fatalf("Watch reported findings for a successful rollout: %+v", findings)
	}
	if !outcome.Succeeded {
		t.Fatalf("Watch did not report success: %+v", outcome)
	}
}

// TestWatchE2E_DaemonSet_HealthyRolloutCompletesWithoutFinding proves the
// positive path before any negative case, the same order already followed
// for StatefulSet: an ordinary DaemonSet with no constraints must complete
// with no Finding at all.
func TestWatchE2E_DaemonSet_HealthyRolloutCompletesWithoutFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	d := deployDaemonSet(t, client, ns, "ds-healthy", sleeperPodSpec(), nil)
	watchDaemonSetAndExpectSuccess(t, client, ns, d, 2*time.Minute)
}

// TestWatchE2E_DaemonSet_CrashLoopApplicationExit verifies that the
// existing, pod-level CrashLoop diagnoser is reused unchanged for
// DaemonSet: it operates on Pods and their ContainerStatuses, which do not
// differ between controller kinds, the same reasoning already proven for
// StatefulSet.
func TestWatchE2E_DaemonSet_CrashLoopApplicationExit(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "exit 7"},
		}},
	}
	d := deployDaemonSet(t, client, ns, "ds-crashy-app-error", podSpec, nil)

	watchDaemonSetAndExpectCause(t, client, ns, d, diagnose.CauseCrashLoopAppError, 2*time.Minute)
}

// TestWatchE2E_DaemonSet_ExhaustedQuota verifies quota-exceeded for
// DaemonSet, whose FailedCreate event lands on the DaemonSet object itself
// (no ReplicaSet exists for this kind either, the same mechanism already
// verified for StatefulSet — daemonSetObserver.podCreationSources in
// internal/diagnose/watch.go).
//
// Unlike the sibling Deployment/StatefulSet scenarios (hard pods: "1"
// against 2 desired replicas, so the second pod is rejected), a DaemonSet
// on this single-node cluster only ever wants ONE pod
// (desiredNumberScheduled tracks the number of eligible nodes, see point 1
// of the file comment above). A "1" pod quota would let that single pod
// through with room to spare, rejecting nothing. hard pods: "0" blocks the
// DaemonSet's one and only desired pod outright — the equivalent
// exhaustion for a workload that never asks for more than one pod here.
func TestWatchE2E_DaemonSet_ExhaustedQuota(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "zero-quota", Namespace: ns},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourcePods: resource.MustParse("0"),
			},
		},
	}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(t.Context(), quota, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ResourceQuota: %v", err)
	}

	d := deployDaemonSet(t, client, ns, "ds-quota-exhausted", sleeperPodSpec(), nil)

	outcome := watchDaemonSetAndExpectCause(t, client, ns, d, diagnose.CauseQuotaExceeded, 2*time.Minute)
	for _, res := range outcome.Results {
		for _, f := range res.Findings {
			if f.CheckID == string(diagnose.CauseQuotaExceeded) && f.Resource.Kind != "DaemonSet" {
				t.Errorf("quota-exceeded finding must reference the DaemonSet itself (no intermediate ReplicaSet exists for this kind), got Resource.Kind=%q", f.Resource.Kind)
			}
		}
	}
}

// TestCheckE2E_PDBDaemonsetScale_MaxUnavailable_RealSyncFailedCondition is
// the load-bearing verification for pdb-daemonset-scale's entire premise.
// Its doc comment (internal/check/pdbdaemonset.go) claims, from reading the
// vendored disruption controller source rather than from observed
// behavior, that a REAL disruption controller reconciling a
// DaemonSet-selecting PodDisruptionBudget with maxUnavailable set fails the
// scale lookup on every single reconcile (DaemonSet pods have no /scale
// subresource) and permanently writes status.conditions with
// Type=DisruptionAllowed, Status=False, Reason=SyncFailed. This scenario
// waits for that exact condition against a live cluster controller instead
// of trusting the source reading: if it never appears, the check's entire
// premise is wrong and must be reported, not silently patched.
func TestCheckE2E_PDBDaemonsetScale_MaxUnavailable_RealSyncFailedCondition(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	d := deployDaemonSet(t, client, ns, "ds-pdb-maxunavailable", sleeperPodSpec(), nil)
	watchDaemonSetAndExpectSuccess(t, client, ns, d, 2*time.Minute)

	maxUnavailable := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-pdb-maxunavailable", Namespace: ns},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &maxUnavailable,
			Selector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ds-pdb-maxunavailable"}},
		},
	}
	if _, err := client.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, pdb, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating PodDisruptionBudget: %v", err)
	}

	var lastSeen *policyv1.PodDisruptionBudget
	pollErr := wait.PollUntilContextTimeout(ctx, 2*time.Second, 90*time.Second, true, func(pollCtx context.Context) (bool, error) {
		live, err := client.PolicyV1().PodDisruptionBudgets(ns).Get(pollCtx, pdb.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		lastSeen = live
		for _, cond := range live.Status.Conditions {
			if cond.Type == policyv1.DisruptionAllowedCondition && cond.Status == metav1.ConditionFalse && cond.Reason == policyv1.SyncFailedReason {
				return true, nil
			}
		}
		return false, nil
	})
	if pollErr != nil {
		if lastSeen != nil {
			t.Fatalf("disruption controller never reported DisruptionAllowed=False/SyncFailed for a maxUnavailable PDB selecting a DaemonSet's pods within the timeout; last observed conditions: %+v (err: %v)", lastSeen.Status.Conditions, pollErr)
		}
		t.Fatalf("polling PodDisruptionBudget %s/%s: %v", ns, pdb.Name, pollErr)
	}

	live, err := client.AppsV1().DaemonSets(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading DaemonSet %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDaemonSet(live), Client: client}
	res, err := check.PDBDaemonsetScale{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (PodDisruptionBudget list is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.Resource.Kind != "PodDisruptionBudget" || f.Resource.Name != pdb.Name {
		t.Errorf("Resource = %+v, want PodDisruptionBudget/%s", f.Resource, pdb.Name)
	}
}

// TestCheckE2E_PDBDaemonsetScale_IntegerMinAvailable_NoFindingRealCluster
// proves the other edge of pdb-daemonset-scale's premise against a live
// controller: an integer minAvailable never enters the scale-lookup code
// path at all (see the check's doc comment), so the disruption controller
// computes disruptionsAllowed directly instead of failing the sync. With
// one DaemonSet pod and minAvailable: 1, the controller should converge on
// DisruptionAllowed=False/Reason=InsufficientPods (evicting the only pod
// would go below minAvailable) rather than ever reporting SyncFailed — a
// positive, checkable outcome, not merely "nothing bad was observed in a
// fixed window".
func TestCheckE2E_PDBDaemonsetScale_IntegerMinAvailable_NoFindingRealCluster(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	d := deployDaemonSet(t, client, ns, "ds-pdb-minavailable", sleeperPodSpec(), nil)
	watchDaemonSetAndExpectSuccess(t, client, ns, d, 2*time.Minute)

	minAvailable := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-pdb-minavailable", Namespace: ns},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ds-pdb-minavailable"}},
		},
	}
	if _, err := client.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, pdb, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating PodDisruptionBudget: %v", err)
	}

	var live *policyv1.PodDisruptionBudget
	pollErr := wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true, func(pollCtx context.Context) (bool, error) {
		got, err := client.PolicyV1().PodDisruptionBudgets(ns).Get(pollCtx, pdb.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		live = got
		for _, cond := range got.Status.Conditions {
			if cond.Type == policyv1.DisruptionAllowedCondition {
				return true, nil
			}
		}
		return false, nil
	})
	if pollErr != nil {
		t.Fatalf("disruption controller never reported a DisruptionAllowed condition within the timeout: %v", pollErr)
	}
	for _, cond := range live.Status.Conditions {
		if cond.Type != policyv1.DisruptionAllowedCondition {
			continue
		}
		if cond.Reason == policyv1.SyncFailedReason {
			t.Fatalf("an integer minAvailable PDB must never hit the scale-lookup SyncFailed path, got condition: %+v", cond)
		}
		if cond.Reason != policyv1.InsufficientPodsReason {
			t.Errorf("condition Reason = %q, want %q (1 pod, minAvailable: 1 leaves no disruption headroom)", cond.Reason, policyv1.InsufficientPodsReason)
		}
	}
	if live.Status.DisruptionsAllowed != 0 {
		t.Errorf("DisruptionsAllowed = %d, want 0 (1 pod, minAvailable: 1)", live.Status.DisruptionsAllowed)
	}

	liveDS, err := client.AppsV1().DaemonSets(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading DaemonSet %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDaemonSet(liveDS), Client: client}
	res, err := check.PDBDaemonsetScale{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("an integer minAvailable PDB must never produce a finding, got %+v", res)
	}
}

// TestCheckE2E_HPAQuotaHeadroom_DaemonSetTarget_NoFinding verifies
// hpa-quota-headroom's DaemonSet short-circuit (internal/check/hpa.go)
// against a real HorizontalPodAutoscaler object: scaleTargetRef.Kind:
// DaemonSet is accepted by the apiserver but can never actually function
// (DaemonSet has no /scale subresource, the same limitation documented on
// pdb-daemonset-scale), so the check must never compute a headroom finding
// on that false premise.
func TestCheckE2E_HPAQuotaHeadroom_DaemonSetTarget_NoFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	d := deployDaemonSet(t, client, ns, "ds-hpa-target", sleeperPodSpec(), nil)
	watchDaemonSetAndExpectSuccess(t, client, ns, d, 2*time.Minute)

	minReplicas := int32(1)
	averageUtilization := int32(80)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-hpa-target", Namespace: ns},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				Kind:       "DaemonSet",
				Name:       d.Name,
				APIVersion: "apps/v1",
			},
			MinReplicas: &minReplicas,
			MaxReplicas: 5,
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: &averageUtilization,
					},
				},
			}},
		},
	}
	if _, err := client.AutoscalingV2().HorizontalPodAutoscalers(ns).Create(ctx, hpa, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating HorizontalPodAutoscaler: %v", err)
	}

	live, err := client.AppsV1().DaemonSets(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading DaemonSet %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDaemonSet(live), Client: client}
	res, err := check.HPAQuotaHeadroom{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("hpa-quota-headroom must never produce a finding for a DaemonSet target, got %+v", res)
	}
}
