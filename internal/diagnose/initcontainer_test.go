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

func podWithInitStatus(status corev1.ContainerStatus) corev1.Pod {
	pod := podWith("app-1", "app-1-uid", corev1.PodPending)
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{status}
	return pod
}

func TestInitContainer_ApplicationError(t *testing.T) {
	terminated := &corev1.ContainerStateTerminated{ExitCode: 3, Reason: "Error"}
	pod := podWithInitStatus(corev1.ContainerStatus{
		Name:                 "migrate",
		RestartCount:         3,
		State:                corev1.ContainerState{Terminated: terminated},
		LastTerminationState: corev1.ContainerState{Terminated: terminated.DeepCopy()},
	})
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)
	target.LogTailer = stubLogTailer{tail: "migration failed\n"}

	res, err := diagnose.InitContainer{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseInitContainerAppError) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseInitContainerAppError, res.Findings)
	}
	f := res.Findings[0]
	if got := f.Remediation.Commands; len(got) != 1 || got[0] != "kubectl logs app-1 -c migrate -n default --previous" {
		t.Fatalf("expected previous init-container log command, got %+v", got)
	}
	evidence := strings.Join(f.Evidence, "\n")
	if !strings.Contains(evidence, "initContainer=migrate") || !strings.Contains(evidence, "exitCode=3") || !strings.Contains(evidence, "reason=Error") || !strings.Contains(evidence, "restartCount=3") {
		t.Fatalf("expected complete structured init-container evidence, got %+v", f.Evidence)
	}
	if strings.Contains(evidence, "migration failed\n") {
		t.Fatalf("log tail must not retain its trailing newline, got %+v", f.Evidence)
	}
}

func TestInitContainer_OOMKilled(t *testing.T) {
	pod := podWithInitStatus(corev1.ContainerStatus{
		Name:         "migrate",
		RestartCount: 2,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CrashLoopBackOff",
		}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 137,
			Reason:   "OOMKilled",
		}},
	})
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.InitContainer{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseInitContainerOOMKilled) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseInitContainerOOMKilled, res.Findings)
	}
}

func TestInitContainer_Undetermined_CrashLoopWithoutTerminationState(t *testing.T) {
	pod := podWithInitStatus(corev1.ContainerStatus{
		Name:         "migrate",
		RestartCount: 1,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CrashLoopBackOff",
		}},
	})
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.InitContainer{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseInitContainerUndetermined) || !f.Undetermined {
		t.Fatalf("expected undetermined init-container finding, got %+v", f)
	}
}

func TestInitContainer_CompletedSuccessfullyProducesNoFinding(t *testing.T) {
	pod := podWithInitStatus(corev1.ContainerStatus{
		Name: "migrate",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 0,
			Reason:   "Completed",
		}},
	})
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.InitContainer{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a completed init container must not produce findings, got %+v", res.Findings)
	}
}

func TestInitContainer_StillRunningProducesNoFinding(t *testing.T) {
	pod := podWithInitStatus(corev1.ContainerStatus{
		Name:  "migrate",
		Ready: false,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})
	target := newTarget(t, []corev1.Pod{pod}, nil, nil)

	res, err := diagnose.InitContainer{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a still-running init container may be slow and must not produce findings, got %+v", res.Findings)
	}
}
