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
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

func crashLoopWaiting() corev1.ContainerStateWaiting {
	return corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}
}

func TestCrashLoop_OOMKilled(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 4,
		State:        corev1.ContainerState{Waiting: ptrWaiting(crashLoopWaiting())},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137,
		}},
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose ha restituito un errore inatteso: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseCrashLoopOOMKilled) {
		t.Errorf("CheckID = %q, atteso %q", f.CheckID, diagnose.CauseCrashLoopOOMKilled)
	}
	if f.Undetermined {
		t.Error("un OOMKill confermato non deve essere Undetermined")
	}
}

func TestCrashLoop_LivenessProbe_HaPrecedenzaSuTerminatedGenerico(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 2,
		State:        corev1.ContainerState{Waiting: ptrWaiting(crashLoopWaiting())},
		// Reason generico "Error": senza l'evento Killing sarebbe
		// indistinguibile da un crash applicativo. E' esattamente il
		// caso che l'ordine di precedenza deve risolvere correttamente.
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 137,
		}},
	})}
	events := []corev1.Event{
		event("app-1-uid", "Killing", "Container app failed liveness probe, will be restarted"),
	}
	target := newTarget(t, pods, events, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose ha restituito un errore inatteso: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseCrashLoopLivenessProbe) {
		t.Errorf("CheckID = %q, atteso %q (l'evento Killing deve avere precedenza)", f.CheckID, diagnose.CauseCrashLoopLivenessProbe)
	}
}

func TestCrashLoop_ErroreApplicativo(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 3,
		State:        corev1.ContainerState{Waiting: ptrWaiting(crashLoopWaiting())},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 1,
		}},
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose ha restituito un errore inatteso: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseCrashLoopAppError) {
		t.Errorf("CheckID = %q, atteso %q", f.CheckID, diagnose.CauseCrashLoopAppError)
	}
	if len(f.Remediation.Commands) == 0 {
		t.Error("atteso un comando di sola lettura (kubectl logs --previous) come guida per la diagnosi")
	}
}

func TestCrashLoop_Undetermined_SenzaLastTerminationState(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Waiting: ptrWaiting(crashLoopWaiting())},
		// LastTerminationState.Terminated volutamente assente.
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose ha restituito un errore inatteso: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseCrashLoopUndetermined) {
		t.Errorf("CheckID = %q, atteso %q", f.CheckID, diagnose.CauseCrashLoopUndetermined)
	}
	if !f.Undetermined {
		t.Error("atteso Undetermined=true quando l'evidenza non basta a distinguere la causa")
	}
	if f.Severity != model.SeverityHigh {
		t.Errorf("un rollout oggettivamente bloccato resta High anche se la causa e' non determinata, got severity=%v", f.Severity)
	}
}

func TestCrashLoop_NessunProblema_ContainerInEsecuzione(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose ha restituito un errore inatteso: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("un container in esecuzione normale non deve produrre finding, got %+v", res.Findings)
	}
}

func ptrWaiting(w corev1.ContainerStateWaiting) *corev1.ContainerStateWaiting { return &w }
