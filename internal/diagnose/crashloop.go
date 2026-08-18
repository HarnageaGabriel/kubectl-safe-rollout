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

package diagnose

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// CrashLoopDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID (not as model.Finding.CheckID: that contains the
// specific CauseID, see cause.go).
const CrashLoopDiagnoserID = "crashloop"

// CrashLoop classifies containers in CrashLoopBackOff, plus containers that
// have restarted after a liveness-probe Killing event, distinguishing OOMKill,
// liveness probe termination, and application errors.
//
// Deliberate precedence order: the liveness probe event is checked before
// ContainerStatus.LastTerminationState because when the probe kills the
// container, the Terminated Reason is often the generic "Error", otherwise
// indistinguishable from an application crash. The "Killing" event that
// mentions the liveness probe is the most specific available signal and
// must be checked first.
type CrashLoop struct{}

// ID implements Diagnoser.
func (CrashLoop) ID() string { return CrashLoopDiagnoserID }

// Diagnose implements Diagnoser.
func (d CrashLoop) Diagnose(ctx context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		for _, cs := range pod.Status.ContainerStatuses {
			inCrashLoopBackOff := cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff"
			if !inCrashLoopBackOff && (cs.RestartCount == 0 || !hasLivenessKilling(target, pod, cs)) {
				continue
			}
			findings = append(findings, d.classify(ctx, target, pod, cs))
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func hasLivenessKilling(target Target, pod corev1.Pod, cs corev1.ContainerStatus) bool {
	for _, e := range target.EventsByUID[pod.UID] {
		if pattern.LivenessKilling(e.Reason, e.Message, cs.Name) {
			return true
		}
	}
	return false
}

func (d CrashLoop) classify(ctx context.Context, target Target, pod corev1.Pod, cs corev1.ContainerStatus) model.Finding {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	base := []string{
		fmt.Sprintf("container=%s", cs.Name),
		fmt.Sprintf("restartCount=%d", cs.RestartCount),
	}

	if f, ok := d.classifyLivenessKilling(target, pod, cs, resource, base); ok {
		return f
	}

	term := cs.LastTerminationState.Terminated
	switch {
	case term == nil:
		evidence := append(append([]string(nil), base...), fmt.Sprintf("container=%s in CrashLoopBackOff, LastTerminationState.Terminated not populated", cs.Name))
		return d.undetermined(resource, evidence)
	case term.Reason == "OOMKilled":
		return d.classifyOOMKilled(pod, cs, term, resource, base)
	case term.Reason != "":
		return d.classifyAppError(ctx, target, pod, cs, term, resource, base)
	default:
		evidence := append(append([]string(nil), base...), fmt.Sprintf("container=%s in CrashLoopBackOff, Terminated.Reason empty", cs.Name))
		return d.undetermined(resource, evidence)
	}
}

func (CrashLoop) classifyLivenessKilling(target Target, pod corev1.Pod, cs corev1.ContainerStatus, resource model.ResourceRef, base []string) (model.Finding, bool) {
	for _, e := range target.EventsByUID[pod.UID] {
		if !pattern.LivenessKilling(e.Reason, e.Message, cs.Name) {
			continue
		}
		evidence := append(append([]string(nil), base...), fmt.Sprintf("event: %s", e.Message))
		return model.Finding{
			CheckID:  string(CauseCrashLoopLivenessProbe),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"container %q in %s is terminated by the kubelet because the liveness probe repeatedly fails, not because the application crashes",
				cs.Name, resource,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"the liveness probe for %q terminates the container before the app is ready: increase initialDelaySeconds/periodSeconds/failureThreshold, or verify that the liveness endpoint responds correctly during startup (consider a startupProbe if startup is slow)",
					cs.Name,
				),
				ContextDependent: true,
			},
			Resource: resource,
		}, true
	}
	return model.Finding{}, false
}

func (CrashLoop) classifyOOMKilled(pod corev1.Pod, cs corev1.ContainerStatus, term *corev1.ContainerStateTerminated, resource model.ResourceRef, base []string) model.Finding {
	evidence := append(append([]string(nil), base...), fmt.Sprintf("exitCode=%d reason=OOMKilled", term.ExitCode))
	return model.Finding{
		CheckID:  string(CauseCrashLoopOOMKilled),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"container %q in %s is killed by the kernel (OOMKilled) for exceeding its memory limit",
			cs.Name, resource,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"increase limits.memory for container %q or reduce the app's memory usage; the correct value depends on the actual usage profile and cannot be safely suggested without context",
				cs.Name,
			),
			ContextDependent: true,
		},
		Resource: resource,
	}
}

// classifyAppError is the only branch that retrieves logs from the
// previous container as additional evidence: this is the case where the
// cause is in the application and there is no structured signal besides
// the exit code, so this is where logs provide real value. The fetch is
// best effort: if it fails or LogTailer is nil, the Finding remains valid
// without that line.
func (CrashLoop) classifyAppError(ctx context.Context, target Target, pod corev1.Pod, cs corev1.ContainerStatus, term *corev1.ContainerStateTerminated, resource model.ResourceRef, base []string) model.Finding {
	evidence := append(append([]string(nil), base...), fmt.Sprintf("exitCode=%d reason=%s", term.ExitCode, term.Reason))
	if target.LogTailer != nil {
		if tail, err := target.LogTailer.PreviousLogTail(ctx, pod.Namespace, pod.Name, cs.Name, 3); err == nil {
			// The log tail keeps the newline of its last line, and the
			// human renderer already writes one per evidence entry: without
			// trimming, every crashloop finding grows a blank line in the
			// middle of its evidence block.
			if tail = strings.TrimRight(tail, "\r\n"); tail != "" {
				evidence = append(evidence, fmt.Sprintf("log --previous (last lines): %s", tail))
			}
		}
	}
	return model.Finding{
		CheckID:  string(CauseCrashLoopAppError),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"container %q in %s exits due to an application error (exit code %d), neither OOMKill nor liveness probe",
			cs.Name, resource, term.ExitCode,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("check the logs for container %q to find the application cause of the exit", cs.Name),
			Commands:         []string{fmt.Sprintf("kubectl logs %s -c %s -n %s --previous", pod.Name, cs.Name, pod.Namespace)},
			ContextDependent: true,
		},
		Resource: resource,
	}
}

func (CrashLoop) undetermined(resource model.ResourceRef, evidence []string) model.Finding {
	return model.Finding{
		CheckID:  string(CauseCrashLoopUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s is in CrashLoopBackOff but the collected evidence does not distinguish OOMKill, liveness probe, or application error", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "collect more context before acting: describe the pod and inspect the previous container logs",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", resource.Name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
}
