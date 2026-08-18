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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
)

func pendingPodWithPVC(claimName string) corev1.Pod {
	p := podWith("app-1", "app-1-uid", corev1.PodPending)
	p.Spec.Volumes = []corev1.Volume{
		{Name: "data", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName},
		}},
	}
	return p
}

func TestPending_PVCUnbound(t *testing.T) {
	pods := []corev1.Pod{pendingPodWithPVC("data-pvc")}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-pvc", Namespace: testNamespace},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	target := newTarget(t, pods, nil, nil, pvc)

	res, err := diagnose.Pending{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CausePendingUnboundPVC) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CausePendingUnboundPVC, res.Findings)
	}
	if res.Findings[0].Undetermined {
		t.Error("a PVC confirmed as not Bound via Status.Phase must not be Undetermined")
	}
}

func TestPending_PVCBound_NotBlocking(t *testing.T) {
	pods := []corev1.Pod{pendingPodWithPVC("data-pvc")}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-pvc", Namespace: testNamespace},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	// No FailedScheduling event: the pod is Pending for another reason
	// that is not observable in this scenario (e.g. it is still about to
	// be scheduled, and there is no event yet). The Bound PVC must not be
	// the reported cause.
	target := newTarget(t, pods, nil, nil, pvc)

	res, err := diagnose.Pending{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("transient Pending with a Bound PVC and no FailedScheduling must not produce findings, got %+v", res.Findings)
	}
}

func TestPending_InsufficientResources(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodPending)}
	events := []corev1.Event{
		event("app-1-uid", "FailedScheduling", "0/5 nodes are available: 5 Insufficient cpu."),
	}
	target := newTarget(t, pods, events, nil)

	res, err := diagnose.Pending{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CausePendingInsufficientResources) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CausePendingInsufficientResources, res.Findings)
	}
}

func TestPending_SchedulingConstraints(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodPending)}
	events := []corev1.Event{
		event("app-1-uid", "FailedScheduling", "0/5 nodes are available: 5 node(s) didn't match Pod's node affinity/selector."),
	}
	target := newTarget(t, pods, events, nil)

	res, err := diagnose.Pending{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CausePendingSchedulingConstraints) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CausePendingSchedulingConstraints, res.Findings)
	}
}

func TestPending_Undetermined_NoRecognizedEvent(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodPending)}
	events := []corev1.Event{
		event("app-1-uid", "FailedScheduling", "scheduler plugin custom: codice non documentato"),
	}
	target := newTarget(t, pods, events, nil)

	res, err := diagnose.Pending{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", res.Findings)
	}
	if !res.Findings[0].Undetermined {
		t.Error("expected Undetermined=true with an unrecognized FailedScheduling event")
	}
}

func TestPending_TransientWithoutFailedScheduling_NoFinding(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodPending)}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.Pending{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("transient Pending without error evidence must not produce findings, got %+v", res.Findings)
	}
}

func TestPending_NoIssue_PodRunning(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning)}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.Pending{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a Running pod must not produce findings, got %+v", res.Findings)
	}
}
