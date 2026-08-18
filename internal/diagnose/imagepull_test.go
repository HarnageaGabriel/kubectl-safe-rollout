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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
)

func imagePullWaiting(reason string) *corev1.ContainerStateWaiting {
	return &corev1.ContainerStateWaiting{Reason: reason}
}

func TestImagePull_TagNotFound(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodPending, corev1.ContainerStatus{
		Name:  "app",
		Image: "registry.example.com/app:v9.9.9",
		State: corev1.ContainerState{Waiting: imagePullWaiting("ImagePullBackOff")},
	})}
	events := []corev1.Event{
		event("app-1-uid", "Failed", `Failed to pull image "registry.example.com/app:v9.9.9": rpc error: manifest unknown`),
	}
	target := newTarget(t, pods, events, nil)

	res, err := diagnose.ImagePull{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseImagePullTagNotFound) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseImagePullTagNotFound, res.Findings)
	}
}

func TestImagePull_CredentialsMissing(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodPending, corev1.ContainerStatus{
		Name:  "app",
		Image: "registry.example.com/private/app:v1",
		State: corev1.ContainerState{Waiting: imagePullWaiting("ErrImagePull")},
	})}
	events := []corev1.Event{
		event("app-1-uid", "Failed", `Failed to pull image "registry.example.com/private/app:v1": rpc error: pull access denied, repository does not exist or may require authorization`),
	}
	target := newTarget(t, pods, events, nil)

	res, err := diagnose.ImagePull{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseImagePullUnauthorized) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseImagePullUnauthorized, res.Findings)
	}
}

func TestImagePull_RegistryUnreachable(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodPending, corev1.ContainerStatus{
		Name:  "app",
		Image: "registry.internal:5000/app:v1",
		State: corev1.ContainerState{Waiting: imagePullWaiting("ImagePullBackOff")},
	})}
	events := []corev1.Event{
		event("app-1-uid", "Failed", `Failed to pull image "registry.internal:5000/app:v1": rpc error: dial tcp: lookup registry.internal: no such host`),
	}
	target := newTarget(t, pods, events, nil)

	res, err := diagnose.ImagePull{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseImagePullRegistryUnreachable) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseImagePullRegistryUnreachable, res.Findings)
	}
}

func TestImagePull_Undetermined_NoRecognizedEvent(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodPending, corev1.ContainerStatus{
		Name:  "app",
		Image: "registry.example.com/app:v1",
		State: corev1.ContainerState{Waiting: imagePullWaiting("ImagePullBackOff")},
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.ImagePull{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseImagePullUndetermined) || !f.Undetermined {
		t.Fatalf("expected an undetermined finding without a recognizable Failed event, got %+v", f)
	}
}

func TestImagePull_NoProblem_ContainerRunning(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:  "app",
		Image: "registry.example.com/app:v1",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.ImagePull{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a normally running container must not produce findings, got %+v", res.Findings)
	}
}
