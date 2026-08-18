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

	corev1 "k8s.io/api/core/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
)

func containerCreatingPod() corev1.Pod {
	return podWith("app-1", "app-1-uid", corev1.PodPending, corev1.ContainerStatus{
		Name: "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "ContainerCreating",
		}},
	})
}

func TestVolumeMount_MissingSecret(t *testing.T) {
	pod := containerCreatingPod()
	message := `MountVolume.SetUp failed for volume "creds" : secret "missing-secret" not found`
	events := []corev1.Event{
		event("app-1-uid", "FailedMount", `MountVolume.SetUp failed for volume "data" : rpc error: code = Unknown`),
		event("app-1-uid", "FailedMount", message),
	}
	target := newTarget(t, []corev1.Pod{pod}, events, nil)

	res, err := diagnose.VolumeMount{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseVolumeMountMissingSecret) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseVolumeMountMissingSecret, res.Findings)
	}
	if !strings.Contains(strings.Join(res.Findings[0].Evidence, "\n"), message) {
		t.Fatalf("FailedMount message must remain verbatim in evidence, got %+v", res.Findings[0].Evidence)
	}
}

func TestVolumeMount_MissingConfigMap(t *testing.T) {
	pod := containerCreatingPod()
	message := `MountVolume.SetUp failed for volume "settings" : configmap "missing-config" not found`
	target := newTarget(t, []corev1.Pod{pod}, []corev1.Event{event("app-1-uid", "FailedMount", message)}, nil)

	res, err := diagnose.VolumeMount{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseVolumeMountMissingConfigMap) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseVolumeMountMissingConfigMap, res.Findings)
	}
}

func TestVolumeMount_Undetermined_PreservesFailedMountMessage(t *testing.T) {
	pod := containerCreatingPod()
	message := `MountVolume.SetUp failed for volume "data" : rpc error: code = Unknown`
	target := newTarget(t, []corev1.Pod{pod}, []corev1.Event{event("app-1-uid", "FailedMount", message)}, nil)

	res, err := diagnose.VolumeMount{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseVolumeMountUndetermined) || !f.Undetermined {
		t.Fatalf("expected undetermined volume mount finding, got %+v", f)
	}
	if !strings.Contains(strings.Join(f.Evidence, "\n"), message) {
		t.Fatalf("FailedMount message must remain verbatim in evidence, got %+v", f.Evidence)
	}
}

func TestVolumeMount_NoEvent_ContainerCreatingProducesNoFinding(t *testing.T) {
	pod := containerCreatingPod()
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.VolumeMount{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("ContainerCreating without a FailedMount event must not produce a guessed finding, got %+v", res.Findings)
	}
}

func TestVolumeMount_NoProblem_RunningContainerIgnoresOldEvent(t *testing.T) {
	pod := podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})
	message := `MountVolume.SetUp failed for volume "creds" : secret "missing-secret" not found`
	target := newTarget(t, []corev1.Pod{pod}, []corev1.Event{event("app-1-uid", "FailedMount", message)}, nil)

	res, err := diagnose.VolumeMount{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a running container must not be blocked by an old FailedMount event, got %+v", res.Findings)
	}
}
