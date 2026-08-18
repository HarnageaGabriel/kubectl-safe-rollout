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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func readinessTarget(t *testing.T, status corev1.ContainerStatus, exceeded bool, events []corev1.Event) diagnose.Target {
	t.Helper()
	pod := podWith("app-1", "app-1-uid", corev1.PodRunning, status)
	target := newTarget(t, []corev1.Pod{pod}, events, nil)
	condition := appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Reason: "ReplicaSetUpdated"}
	if exceeded {
		condition.Reason = "ProgressDeadlineExceeded"
		condition.Message = `ReplicaSet "app-abc123" has timed out progressing.`
	}
	target.Workload = workload.FromDeployment(deploymentWithConditions(condition))
	return target
}

func runningContainer(ready bool) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:         "app",
		Ready:        ready,
		RestartCount: 2,
		State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}
}

func TestReadiness_NotReadyBeforeDeadline_NoFinding(t *testing.T) {
	events := []corev1.Event{event("app-1-uid", "Unhealthy", "Readiness probe failed: ")}
	target := readinessTarget(t, runningContainer(false), false, events)

	res, err := diagnose.Readiness{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatal("a rollout still progressing is evaluable and must not be skipped")
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a temporarily unready container before the deadline must not produce findings, got %+v", res.Findings)
	}
}

func TestReadiness_ProbeFailingAfterDeadline_High(t *testing.T) {
	message := "Readiness probe failed: HTTP probe failed with statuscode: 500"
	events := []corev1.Event{event("app-1-uid", "Unhealthy", message)}
	target := readinessTarget(t, runningContainer(false), true, events)

	res, err := diagnose.Readiness{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 readiness finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseReadinessProbeFailing) || f.Severity != model.SeverityHigh || f.Undetermined {
		t.Fatalf("expected determined High finding %q, got %+v", diagnose.CauseReadinessProbeFailing, f)
	}
	evidence := strings.Join(f.Evidence, "\n")
	for _, want := range []string{"container=app", "state=running ready=false", "restartCount=2", message} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("evidence must contain %q, got %+v", want, f.Evidence)
		}
	}
	if got := f.Remediation.Commands; len(got) != 2 || got[0] != "kubectl describe pod app-1 -n default" || got[1] != "kubectl logs app-1 -c app -n default" {
		t.Fatalf("expected read-only describe and logs commands, got %+v", got)
	}
	if !f.Remediation.ContextDependent || !strings.Contains(f.Remediation.Summary, "wrong path or port") || !strings.Contains(f.Remediation.Summary, "genuinely not serving") {
		t.Fatalf("remediation must explain the indistinguishable causes and remain context-dependent, got %+v", f.Remediation)
	}
}

func TestReadiness_UndeterminedAfterDeadlineWithoutEvent(t *testing.T) {
	target := readinessTarget(t, runningContainer(false), true, nil)

	res, err := diagnose.Readiness{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 readiness finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != string(diagnose.CauseReadinessUndetermined) || f.Severity != model.SeverityHigh || !f.Undetermined {
		t.Fatalf("expected undetermined High finding %q, got %+v", diagnose.CauseReadinessUndetermined, f)
	}
	evidence := strings.Join(f.Evidence, "\n")
	if !strings.Contains(evidence, "running and not ready after the rollout exceeded its progress deadline") || !strings.Contains(evidence, "no Unhealthy event") {
		t.Fatalf("evidence must state the deadline-qualified unready state and missing event, got %+v", f.Evidence)
	}
}

func TestReadiness_ReadyAfterDeadline_NoFinding(t *testing.T) {
	target := readinessTarget(t, runningContainer(true), true, nil)

	res, err := diagnose.Readiness{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a ready container must not produce findings after the deadline, got %+v", res.Findings)
	}
}

func TestReadiness_WaitingAfterDeadline_NoFinding(t *testing.T) {
	status := corev1.ContainerStatus{
		Name:  "app",
		Ready: false,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "CrashLoopBackOff",
		}},
	}
	target := readinessTarget(t, status, true, nil)

	res, err := diagnose.Readiness{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a waiting container belongs to another Diagnoser, got %+v", res.Findings)
	}
}
