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
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

func configErrorPod(message string) corev1.Pod {
	return podWith("app-1", "app-1-uid", corev1.PodPending, corev1.ContainerStatus{
		Name: "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason:  "CreateContainerConfigError",
			Message: message,
		}},
	})
}

func TestConfigError_MissingConfigMap(t *testing.T) {
	pod := configErrorPod(`configmap "does-not-exist" not found`)
	events := []corev1.Event{
		event("app-1-uid", "Failed", `Error: secret "unrelated" not found`),
		event("app-1-uid", "Failed", `Error: configmap "does-not-exist" not found`),
	}
	target := newTarget(t, []corev1.Pod{pod}, events, nil)

	res, err := diagnose.ConfigError{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseConfigErrorMissingConfigMap) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseConfigErrorMissingConfigMap, res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh || f.Undetermined {
		t.Fatalf("a confirmed missing ConfigMap must be determined High, got %+v", f)
	}
	if len(f.Remediation.Commands) != 1 || f.Remediation.Commands[0] != "kubectl get configmap does-not-exist -n default" || !f.Remediation.ContextDependent {
		t.Fatalf("expected read-only ConfigMap lookup with context-dependent remediation, got %+v", f.Remediation)
	}
	if !strings.Contains(strings.Join(f.Evidence, "\n"), `event: Error: configmap "does-not-exist" not found`) {
		t.Fatalf("expected matching Failed event evidence, got %+v", f.Evidence)
	}
}

func TestConfigError_MissingSecret(t *testing.T) {
	pod := configErrorPod(`secret "missing-secret" not found`)
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.ConfigError{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseConfigErrorMissingSecret) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseConfigErrorMissingSecret, res.Findings)
	}
	if got := res.Findings[0].Remediation.Commands; len(got) != 1 || got[0] != "kubectl get secret missing-secret -n default" {
		t.Fatalf("expected read-only Secret lookup, got %+v", got)
	}
}

func TestConfigError_Undetermined_PreservesWaitingMessage(t *testing.T) {
	message := `couldn't find key DATABASE_URL in Secret default/app-settings`
	pod := configErrorPod(message)
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.ConfigError{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseConfigErrorUndetermined) || !f.Undetermined {
		t.Fatalf("expected undetermined container configuration finding, got %+v", f)
	}
	if !strings.Contains(strings.Join(f.Evidence, "\n"), message) {
		t.Fatalf("waiting message must remain verbatim in evidence, got %+v", f.Evidence)
	}
}

func TestConfigError_NoProblem_ContainerRunning(t *testing.T) {
	pod := podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.ConfigError{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a normally running container must not produce findings, got %+v", res.Findings)
	}
}

func TestConfigError_MissingSecretInInitContainer(t *testing.T) {
	pod := podWith("app-1", "app-1-uid", corev1.PodPending)
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: "migrate",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason:  "CreateContainerConfigError",
			Message: `secret "migration-credentials" not found`,
		}},
	}}
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.ConfigError{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseConfigErrorMissingSecret) {
		t.Fatalf("expected init-container configuration finding %q, got %+v", diagnose.CauseConfigErrorMissingSecret, res.Findings)
	}
}
