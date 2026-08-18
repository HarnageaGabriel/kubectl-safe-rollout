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
	"context"
	"strings"
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
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseCrashLoopOOMKilled) {
		t.Errorf("CheckID = %q, expected %q", f.CheckID, diagnose.CauseCrashLoopOOMKilled)
	}
	if f.Undetermined {
		t.Error("a confirmed OOMKill must not be Undetermined")
	}
}

func TestCrashLoop_LivenessProbe_TakesPrecedenceOverGenericTerminated(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 2,
		State:        corev1.ContainerState{Waiting: ptrWaiting(crashLoopWaiting())},
		// Generic "Error" Reason: without the Killing event it would be
		// indistinguishable from an application crash. This is exactly the
		// case that the precedence order must resolve correctly.
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
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseCrashLoopLivenessProbe) {
		t.Errorf("CheckID = %q, expected %q (the Killing event must take precedence)", f.CheckID, diagnose.CauseCrashLoopLivenessProbe)
	}
}

func TestCrashLoop_LivenessProbeKillingWithoutCrashLoopBackOff(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 2,
		State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})}
	events := []corev1.Event{
		event("app-1-uid", "Killing", "Container app failed liveness probe, will be restarted"),
	}
	target := newTarget(t, pods, events, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	if res.Findings[0].CheckID != string(diagnose.CauseCrashLoopLivenessProbe) {
		t.Errorf("CheckID = %q, expected %q", res.Findings[0].CheckID, diagnose.CauseCrashLoopLivenessProbe)
	}
}

func TestCrashLoop_RestartWithoutLivenessEventIsNotEnough(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 1,
		State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a restart without a liveness Killing event must not produce findings, got %+v", res.Findings)
	}
}

func TestCrashLoop_ApplicationError(t *testing.T) {
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
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseCrashLoopAppError) {
		t.Errorf("CheckID = %q, expected %q", f.CheckID, diagnose.CauseCrashLoopAppError)
	}
	if len(f.Remediation.Commands) == 0 {
		t.Error("expected a read-only command (kubectl logs --previous) as diagnosis guidance")
	}
}

func TestCrashLoop_Undetermined_WithoutLastTerminationState(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Waiting: ptrWaiting(crashLoopWaiting())},
		// LastTerminationState.Terminated deliberately absent.
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseCrashLoopUndetermined) {
		t.Errorf("CheckID = %q, expected %q", f.CheckID, diagnose.CauseCrashLoopUndetermined)
	}
	if !f.Undetermined {
		t.Error("expected Undetermined=true when evidence is insufficient to distinguish the cause")
	}
	if f.Severity != model.SeverityHigh {
		t.Errorf("an objectively blocked rollout remains High even if the cause is undetermined, got severity=%v", f.Severity)
	}
}

func TestCrashLoop_NoProblem_ContainerRunning(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})}
	target := newTarget(t, pods, nil, nil)

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a normally running container must not produce findings, got %+v", res.Findings)
	}
}

func ptrWaiting(w corev1.ContainerStateWaiting) *corev1.ContainerStateWaiting { return &w }

// stubLogTailer returns a canned previous-log tail, including the trailing
// newline that a real container log ends with.
type stubLogTailer struct{ tail string }

func (s stubLogTailer) PreviousLogTail(_ context.Context, _, _, _ string, _ int64) (string, error) {
	return s.tail, nil
}

func TestCrashLoop_ApplicationError_LogTailTrimmed(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 1,
		State:        corev1.ContainerState{Waiting: ptrWaiting(crashLoopWaiting())},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 1,
		}},
	})}
	target := newTarget(t, pods, nil, nil)
	target.LogTailer = stubLogTailer{tail: "config file not found\n"}

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}

	var logEvidence string
	for _, e := range res.Findings[0].Evidence {
		if strings.HasPrefix(e, "log --previous") {
			logEvidence = e
		}
	}
	if logEvidence == "" {
		t.Fatalf("expected log evidence, got %+v", res.Findings[0].Evidence)
	}
	// A trailing newline would render as a blank line inside the evidence
	// block, since the human renderer already writes one per entry.
	if strings.ContainsAny(logEvidence, "\r\n") {
		t.Errorf("log evidence must not carry line breaks, got %q", logEvidence)
	}
}

func TestCrashLoop_ApplicationError_BlankLogTailOmitted(t *testing.T) {
	pods := []corev1.Pod{podWith("app-1", "app-1-uid", corev1.PodRunning, corev1.ContainerStatus{
		Name:         "app",
		RestartCount: 1,
		State:        corev1.ContainerState{Waiting: ptrWaiting(crashLoopWaiting())},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 1,
		}},
	})}
	target := newTarget(t, pods, nil, nil)
	target.LogTailer = stubLogTailer{tail: "\n\n"}

	res, err := diagnose.CrashLoop{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	for _, e := range res.Findings[0].Evidence {
		if strings.HasPrefix(e, "log --previous") {
			t.Errorf("a log tail of only line breaks must not become evidence, got %q", e)
		}
	}
}
