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

// Shared fixtures for internal/diagnose tests: realistic observed state
// (Pod, ContainerStatus, Event) makes these tests credible evidence of
// classification, not merely evidence that the code compiles.
package diagnose_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

const testNamespace = "default"

func int32Ptr(v int32) *int32 { return &v }

func podWith(name string, uid types.UID, phase corev1.PodPhase, statuses ...corev1.ContainerStatus) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: uid},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: statuses,
		},
	}
}

func event(uid types.UID, reason, message string) corev1.Event {
	return corev1.Event{
		InvolvedObject: corev1.ObjectReference{UID: uid},
		Reason:         reason,
		Message:        message,
	}
}

// newTarget builds a test diagnose.Target. extraObjs populates the fake
// clientset (used by the Pending Diagnoser to read the
// PersistentVolumeClaims referenced by the pods).
func newTarget(t *testing.T, pods []corev1.Pod, events []corev1.Event, replicaSets []appsv1.ReplicaSet, extraObjs ...runtime.Object) diagnose.Target {
	t.Helper()
	client := fake.NewSimpleClientset(extraObjs...)
	return diagnose.Target{
		Namespace:   testNamespace,
		Client:      client,
		Pods:        pods,
		ReplicaSets: replicaSets,
		EventsByUID: diagnose.GroupEventsByInvolvedObject(events),
	}
}

func TestGroupEventsByInvolvedObject_GroupsByUID(t *testing.T) {
	events := []corev1.Event{
		event("pod-a", "Failed", "first"),
		event("pod-a", "Failed", "second"),
		event("pod-b", "Failed", "third"),
		event("", "Failed", "no involved object, must be ignored"),
	}

	grouped := diagnose.GroupEventsByInvolvedObject(events)

	if len(grouped["pod-a"]) != 2 {
		t.Fatalf("expected 2 events for pod-a, got %d", len(grouped["pod-a"]))
	}
	if len(grouped["pod-b"]) != 1 {
		t.Fatalf("expected 1 event for pod-b, got %d", len(grouped["pod-b"]))
	}
	if _, ok := grouped[""]; ok {
		t.Fatalf("an event without InvolvedObject.UID must not appear in the map")
	}
}

func TestAnyFindings(t *testing.T) {
	if diagnose.AnyFindings([]diagnose.Result{{DiagnoserID: "x"}}) {
		t.Fatal("AnyFindings must be false when no Result has Findings")
	}
	withFinding := []diagnose.Result{{DiagnoserID: "x", Findings: []model.Finding{{CheckID: "y"}}}}
	if !diagnose.AnyFindings(withFinding) {
		t.Fatal("AnyFindings must be true when at least one Result has Findings")
	}
}

func TestAllFindings_Flattens(t *testing.T) {
	results := []diagnose.Result{
		{DiagnoserID: "a", Findings: []model.Finding{{CheckID: "a1"}, {CheckID: "a2"}}},
		{DiagnoserID: "b", Findings: []model.Finding{{CheckID: "b1"}}},
	}
	got := diagnose.AllFindings(results)
	if len(got) != 3 {
		t.Fatalf("expected 3 flattened findings, got %d", len(got))
	}
}

func TestRunDiagnosis_NoIssue(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})}
	target := newTarget(t, pods, nil, nil)
	target.Workload = workload.FromDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(1)},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
	})

	results, err := diagnose.RunDiagnosis(t.Context(), target)
	if err != nil {
		t.Fatalf("RunDiagnosis returned an unexpected error: %v", err)
	}
	if diagnose.AnyFindings(results) {
		t.Fatalf("expected no issues, got %+v", diagnose.AllFindings(results))
	}
}

func TestRunDiagnosis_CrashLoopDetected(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name: "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CrashLoopBackOff",
		}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137,
		}},
	})}
	target := newTarget(t, pods, nil, nil)
	target.Workload = workload.FromDeployment(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(1)},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
	})

	results, err := diagnose.RunDiagnosis(t.Context(), target)
	if err != nil {
		t.Fatalf("RunDiagnosis returned an unexpected error: %v", err)
	}
	if !diagnose.AnyFindings(results) {
		t.Fatal("expected at least one finding for the pod in CrashLoopBackOff/OOMKilled")
	}
}
