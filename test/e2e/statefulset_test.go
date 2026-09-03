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

package e2e_test

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// deployStatefulSet creates a single-container StatefulSet with matching
// selector labels in Spec.Selector and Template, mirroring deployWorkload's
// shape for Deployment. It also creates the headless Service that
// spec.serviceName names: the API server does not actually validate that
// the Service exists (only that the name is a valid DNS label), but a
// dangling reference would leave every scenario relying on an
// unverified assumption about server-side validation instead of mirroring
// how a StatefulSet is really deployed.
func deployStatefulSet(t *testing.T, client kubernetes.Interface, namespace, name string, replicas int32, podSpec corev1.PodSpec, mutate func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	t.Helper()
	ctx := context.Background()
	labels := map[string]string{"app": name}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  labels,
			Ports:     []corev1.ServicePort{{Port: 80}},
		},
	}
	if _, err := client.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating headless Service %s/%s: %v", namespace, name, err)
	}

	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: name,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
	if mutate != nil {
		mutate(s)
	}
	created, err := client.AppsV1().StatefulSets(namespace).Create(ctx, s, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating StatefulSet %s/%s: %v", namespace, name, err)
	}
	return created
}

// updateStatefulSet reads the live object, applies mutate, and writes it
// back. Used between two Watch calls in a scenario to move a StatefulSet
// from "settled" to "has a pending update", the same two-step shape a real
// operator follows (deploy, let it settle, then push a change).
func updateStatefulSet(t *testing.T, client kubernetes.Interface, namespace, name string, mutate func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	t.Helper()
	ctx := context.Background()
	live, err := client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading StatefulSet %s/%s before update: %v", namespace, name, err)
	}
	mutate(live)
	updated, err := client.AppsV1().StatefulSets(namespace).Update(ctx, live, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("updating StatefulSet %s/%s: %v", namespace, name, err)
	}
	return updated
}

// watchStatefulSetAndExpectCause mirrors watchAndExpectCause, for a
// StatefulSet instead of a Deployment. It returns the Outcome (unlike
// watchAndExpectCause) so a scenario can assert on more than the CauseID
// alone, e.g. which Resource.Kind the Finding names.
func watchStatefulSetAndExpectCause(t *testing.T, client kubernetes.Interface, namespace string, s *appsv1.StatefulSet, causeID diagnose.CauseID, timeout time.Duration) diagnose.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome, err := diagnose.Watch(ctx, diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  workload.FromStatefulSet(s),
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

// watchStatefulSetAndExpectSuccess mirrors watchAndExpectSuccess, for a
// StatefulSet instead of a Deployment.
func watchStatefulSetAndExpectSuccess(t *testing.T, client kubernetes.Interface, namespace string, s *appsv1.StatefulSet, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome, err := diagnose.Watch(ctx, diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  workload.FromStatefulSet(s),
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

// sleeperPodSpec is the plain, always-running container shared by most
// scenarios below: they classify a StatefulSet-level or controller-level
// condition, not a pod-level failure mode, so the pod itself only needs to
// start and stay Ready.
func sleeperPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "sleep 3600"},
		}},
	}
}

// TestWatchE2E_StatefulSet_CrashLoopApplicationExit verifies that the
// existing, pod-level CrashLoop diagnoser is reused unchanged for
// StatefulSet: it operates on Pods and their ContainerStatuses, which do
// not differ between the two controller kinds.
func TestWatchE2E_StatefulSet_CrashLoopApplicationExit(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "exit 7"},
		}},
	}
	s := deployStatefulSet(t, client, ns, "sts-crashy-app-error", 1, podSpec, nil)

	watchStatefulSetAndExpectCause(t, client, ns, s, diagnose.CauseCrashLoopAppError, 2*time.Minute)
}

// TestWatchE2E_StatefulSet_OrderedRollingUpdateCompletesWithoutFinding is a
// false-positive guard against StatefulSet's update cadence: RollingUpdate
// replaces pods one ordinal at a time (deleting the old one before creating
// its replacement), never in parallel like Deployment's surge. A 3-replica
// update takes noticeably longer in wall-clock time than the equivalent
// Deployment rollout; this must still complete with no Finding.
func TestWatchE2E_StatefulSet_OrderedRollingUpdateCompletesWithoutFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	s := deployStatefulSet(t, client, ns, "sts-ordered-update", 3, sleeperPodSpec(), nil)
	watchStatefulSetAndExpectSuccess(t, client, ns, s, 2*time.Minute)

	updated := updateStatefulSet(t, client, ns, s.Name, func(live *appsv1.StatefulSet) {
		live.Spec.Template.Spec.Containers[0].Command = []string{"sh", "-c", "sleep 7200"}
	})

	watchStatefulSetAndExpectSuccess(t, client, ns, updated, 3*time.Minute)
}

// TestWatchE2E_StatefulSet_OnDeletePendingUpdate verifies
// statefulset-update-ondelete: a StatefulSet using the OnDelete strategy has
// a pending update (a pod template change moved status.updateRevision away
// from status.currentRevision) that the controller will never act on by
// itself. The StatefulSet is first let settle on its initial revision (so
// updateRevision==currentRevision going in) before the template is changed,
// mirroring the real sequence: deploy, let it stabilize, then push a change.
func TestWatchE2E_StatefulSet_OnDeletePendingUpdate(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	s := deployStatefulSet(t, client, ns, "sts-ondelete-pending", 2, sleeperPodSpec(), func(s *appsv1.StatefulSet) {
		s.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	})
	watchStatefulSetAndExpectSuccess(t, client, ns, s, 2*time.Minute)

	updated := updateStatefulSet(t, client, ns, s.Name, func(live *appsv1.StatefulSet) {
		live.Spec.Template.Spec.Containers[0].Command = []string{"sh", "-c", "sleep 7200"}
	})

	watchStatefulSetAndExpectCause(t, client, ns, updated, diagnose.CauseStatefulSetUpdateOnDelete, 2*time.Minute)
}

// TestWatchE2E_StatefulSet_PartitionAtOrAboveReplicas verifies
// statefulset-partition-blocked: a RollingUpdate partition set to (or above)
// the replica count makes every pod ordinal ineligible for the pending
// update, so the controller can never reach the new revision on its own.
func TestWatchE2E_StatefulSet_PartitionAtOrAboveReplicas(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	s := deployStatefulSet(t, client, ns, "sts-partition-blocked", 2, sleeperPodSpec(), nil)
	watchStatefulSetAndExpectSuccess(t, client, ns, s, 2*time.Minute)

	partition := int32(2)
	updated := updateStatefulSet(t, client, ns, s.Name, func(live *appsv1.StatefulSet) {
		live.Spec.Template.Spec.Containers[0].Command = []string{"sh", "-c", "sleep 7200"}
		live.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
		}
	})

	watchStatefulSetAndExpectCause(t, client, ns, updated, diagnose.CauseStatefulSetPartitionBlocked, 2*time.Minute)
}

// TestWatchE2E_StatefulSet_CanaryPartitionCompletesWithoutFinding is the
// defining false-positive guard for the statefulset-update Diagnoser: 0 <
// partition < replicas is the standard canary idiom (only pods with an
// ordinal >= partition ever update), deliberate and healthy, not a stuck
// rollout. internal/diagnose/statefulsetupdate_test.go already proves this
// against a fake clientset; this scenario is what proves it against a REAL
// StatefulSet controller, including its own RolloutComplete() threshold
// (desired-partition updated replicas), not a fixture guess about how the
// controller behaves.
func TestWatchE2E_StatefulSet_CanaryPartitionCompletesWithoutFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	s := deployStatefulSet(t, client, ns, "sts-canary-partition", 4, sleeperPodSpec(), nil)
	watchStatefulSetAndExpectSuccess(t, client, ns, s, 2*time.Minute)

	partition := int32(2)
	updated := updateStatefulSet(t, client, ns, s.Name, func(live *appsv1.StatefulSet) {
		live.Spec.Template.Spec.Containers[0].Command = []string{"sh", "-c", "sleep 7200"}
		live.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
		}
	})

	watchStatefulSetAndExpectSuccess(t, client, ns, updated, 2*time.Minute)
}

// TestWatchE2E_StatefulSet_ExhaustedQuota verifies quota-exceeded for
// StatefulSet, and specifically the mechanism that differs from Deployment:
// the StatefulSet controller creates Pods directly (no intermediate
// ReplicaSet), so the FailedCreate event from the quota admission rejection
// lands on the StatefulSet object itself (statefulSetObserver.
// podCreationSources in internal/diagnose/watch.go), not on a child object.
// This is asserted directly via Resource.Kind, not only via the CauseID.
func TestWatchE2E_StatefulSet_ExhaustedQuota(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "tight-quota", Namespace: ns},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourcePods: resource.MustParse("1"),
			},
		},
	}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(t.Context(), quota, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ResourceQuota: %v", err)
	}

	s := deployStatefulSet(t, client, ns, "sts-quota-exhausted", 2, sleeperPodSpec(), nil)

	outcome := watchStatefulSetAndExpectCause(t, client, ns, s, diagnose.CauseQuotaExceeded, 2*time.Minute)
	for _, res := range outcome.Results {
		for _, f := range res.Findings {
			if f.CheckID == string(diagnose.CauseQuotaExceeded) && f.Resource.Kind != "StatefulSet" {
				t.Errorf("quota-exceeded finding must reference the StatefulSet itself (no intermediate ReplicaSet exists for this kind), got Resource.Kind=%q", f.Resource.Kind)
			}
		}
	}
}
