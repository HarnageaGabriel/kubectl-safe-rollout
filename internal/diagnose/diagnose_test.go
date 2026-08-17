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

// Fixture condivise per i test di internal/diagnose: uno stato osservato
// realistico (Pod, ContainerStatus, Event) e' quello che rende questi
// test una prova credibile della classificazione, non solo del fatto che
// il codice compili.
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

// newTarget costruisce un diagnose.Target di test. extraObjs alimenta il
// fake clientset (usato dal Diagnoser Pending per leggere le
// PersistentVolumeClaim referenziate dai pod).
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

func TestGroupEventsByInvolvedObject_RaggruppaPerUID(t *testing.T) {
	events := []corev1.Event{
		event("pod-a", "Failed", "primo"),
		event("pod-a", "Failed", "secondo"),
		event("pod-b", "Failed", "terzo"),
		event("", "Failed", "senza involved object, va ignorato"),
	}

	grouped := diagnose.GroupEventsByInvolvedObject(events)

	if len(grouped["pod-a"]) != 2 {
		t.Fatalf("attesi 2 eventi per pod-a, got %d", len(grouped["pod-a"]))
	}
	if len(grouped["pod-b"]) != 1 {
		t.Fatalf("atteso 1 evento per pod-b, got %d", len(grouped["pod-b"]))
	}
	if _, ok := grouped[""]; ok {
		t.Fatalf("un evento senza InvolvedObject.UID non deve comparire nella mappa")
	}
}

func TestAnyFindings(t *testing.T) {
	if diagnose.AnyFindings([]diagnose.Result{{DiagnoserID: "x"}}) {
		t.Fatal("AnyFindings deve essere false se nessun Result ha Findings")
	}
	withFinding := []diagnose.Result{{DiagnoserID: "x", Findings: []model.Finding{{CheckID: "y"}}}}
	if !diagnose.AnyFindings(withFinding) {
		t.Fatal("AnyFindings deve essere true se almeno un Result ha Findings")
	}
}

func TestAllFindings_Appiattisce(t *testing.T) {
	results := []diagnose.Result{
		{DiagnoserID: "a", Findings: []model.Finding{{CheckID: "a1"}, {CheckID: "a2"}}},
		{DiagnoserID: "b", Findings: []model.Finding{{CheckID: "b1"}}},
	}
	got := diagnose.AllFindings(results)
	if len(got) != 3 {
		t.Fatalf("attesi 3 finding appiattiti, got %d", len(got))
	}
}

func TestRunDiagnosis_NessunProblema(t *testing.T) {
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
		t.Fatalf("RunDiagnosis ha restituito un errore inatteso: %v", err)
	}
	if diagnose.AnyFindings(results) {
		t.Fatalf("nessun problema atteso, got %+v", diagnose.AllFindings(results))
	}
}

func TestRunDiagnosis_CrashLoopRilevato(t *testing.T) {
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
		t.Fatalf("RunDiagnosis ha restituito un errore inatteso: %v", err)
	}
	if !diagnose.AnyFindings(results) {
		t.Fatal("atteso almeno un finding per il pod in CrashLoopBackOff/OOMKilled")
	}
}
