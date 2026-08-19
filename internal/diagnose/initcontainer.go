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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// InitContainerDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const InitContainerDiagnoserID = "init-container"

// InitContainer classifies init containers that repeatedly fail before the
// Pod's application containers can start.
type InitContainer struct{}

// ID implements Diagnoser.
func (InitContainer) ID() string { return InitContainerDiagnoserID }

// Diagnose implements Diagnoser.
func (d InitContainer) Diagnose(ctx context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		for _, cs := range pod.Status.InitContainerStatuses {
			term, failed := failedInitTermination(cs)
			if !failed {
				// Exit code 0 means initialization completed. A currently
				// running init container may only be a slow migration; neither
				// state is evidence of failure.
				continue
			}
			findings = append(findings, d.classify(ctx, target, pod, cs, term))
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func failedInitTermination(cs corev1.ContainerStatus) (*corev1.ContainerStateTerminated, bool) {
	if term := cs.State.Terminated; term != nil && term.ExitCode != 0 && cs.RestartCount > 0 {
		return term, true
	}
	if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
		return cs.LastTerminationState.Terminated, true
	}
	return nil, false
}

func (d InitContainer) classify(ctx context.Context, target Target, pod corev1.Pod, cs corev1.ContainerStatus, term *corev1.ContainerStateTerminated) model.Finding {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	exitCode := "unknown"
	reason := "unknown"
	if term != nil {
		exitCode = fmt.Sprintf("%d", term.ExitCode)
		reason = term.Reason
	}
	evidence := []string{
		fmt.Sprintf("initContainer=%s", cs.Name),
		fmt.Sprintf("exitCode=%s", exitCode),
		fmt.Sprintf("reason=%s", reason),
		fmt.Sprintf("restartCount=%d", cs.RestartCount),
	}
	if target.LogTailer != nil {
		if tail, err := target.LogTailer.PreviousLogTail(ctx, pod.Namespace, pod.Name, cs.Name, 3); err == nil {
			// Previous logs retain their final newline, while the human
			// renderer adds one for each evidence entry.
			if tail = strings.TrimRight(tail, "\r\n"); tail != "" {
				evidence = append(evidence, fmt.Sprintf("log --previous (last lines): %s", tail))
			}
		}
	}

	switch {
	case term != nil && term.Reason == "OOMKilled":
		// Unlike the application-error and undetermined cases, the cause is
		// already known here: the kernel killed the container for exceeding
		// its memory limit. Suggesting "check the logs" would send the reader
		// looking for an explanation that does not exist there (observed on
		// kind: an init container OOMKilled mid-write leaves only whatever it
		// had already flushed, never a reason). The remediation matches
		// CrashLoop's classifyOOMKilled for the same reason: raising a memory
		// limit is a business decision this tool cannot make safely, so it
		// names the fix without prescribing a number.
		return model.Finding{
			CheckID:  string(CauseInitContainerOOMKilled),
			Severity: model.SeverityHigh,
			Cause:    fmt.Sprintf("init container %q in %s is killed by the kernel (OOMKilled) before initialization completes", cs.Name, resource),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("increase limits.memory for init container %q or reduce what it allocates during initialization; the correct value depends on the actual usage profile and cannot be safely suggested without context", cs.Name),
				ContextDependent: true,
			},
			Resource: resource,
		}
	case term != nil && term.ExitCode != 0:
		return d.logBasedFinding(resource, pod, cs, CauseInitContainerAppError,
			fmt.Sprintf("init container %q in %s exits with application error code %d before initialization completes", cs.Name, resource, term.ExitCode), evidence, false)
	default:
		return d.logBasedFinding(resource, pod, cs, CauseInitContainerUndetermined,
			fmt.Sprintf("init container %q in %s is in CrashLoopBackOff but its termination state does not identify the cause", cs.Name, resource), evidence, true)
	}
}

// logBasedFinding is for causes where the previous log tail is the only lead
// available: an application error the container itself explains, or no known
// cause at all. It is not used for OOMKilled, where the fix is already known
// and pointing at logs would be a wrong suggestion, not just a redundant one.
func (InitContainer) logBasedFinding(resource model.ResourceRef, pod corev1.Pod, cs corev1.ContainerStatus, causeID CauseID, cause string, evidence []string, undetermined bool) model.Finding {
	return model.Finding{
		CheckID:  string(causeID),
		Severity: model.SeverityHigh,
		Cause:    cause,
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("inspect the previous logs for init container %q to identify why initialization cannot complete", cs.Name),
			Commands:         []string{fmt.Sprintf("kubectl logs %s -c %s -n %s --previous", pod.Name, cs.Name, pod.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: undetermined,
	}
}
