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

	corev1 "k8s.io/api/core/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ReadinessDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const ReadinessDiagnoserID = "readiness"

// Readiness classifies application containers that remain running but not
// ready only after the Deployment controller has marked the rollout as not
// progressing. This deadline gate is deliberate: every healthy rollout has
// temporarily unready pods, and a slow-starting application may legitimately
// fail its readiness probe several times before becoming ready. Reporting on
// Ready==false or an Unhealthy event alone would therefore create false
// positives for rollouts that are about to succeed. Once
// Workload.ProgressDeadlineExceeded() reports true, the controller has
// established the failure; this Diagnoser only identifies which containers
// remain unready and whether a readiness-probe event explains why. The gate
// must not be simplified to pod readiness or event presence alone.
type Readiness struct{}

// ID implements Diagnoser.
func (Readiness) ID() string { return ReadinessDiagnoserID }

// Diagnose implements Diagnoser.
func (d Readiness) Diagnose(_ context.Context, target Target) (Result, error) {
	if target.Workload.Kind() == "StatefulSet" {
		// The progress-deadline gate this Diagnoser depends on never opens
		// for StatefulSet (see ProgressDeadline's own Skip): without this
		// explicit Skip, Readiness would stay permanently, silently empty
		// for the entire kind, indistinguishable from "evaluated every
		// container and found none unready".
		return SkipResult(d.ID(), "readiness classification depends on progress-deadline evaluation, which is not available for this kind"), nil
	}
	if _, exceeded := target.Workload.ProgressDeadlineExceeded(); !exceeded {
		return Result{DiagnoserID: d.ID()}, nil
	}

	var findings []model.Finding
	for _, pod := range target.Pods {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Running == nil || cs.Ready {
				// Waiting and terminated containers are classified by other
				// Diagnosers; including them here would double-report one failure.
				continue
			}
			findings = append(findings, d.classify(pod, cs, target.EventsByUID[pod.UID]))
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func (d Readiness) classify(pod corev1.Pod, cs corev1.ContainerStatus, events []corev1.Event) model.Finding {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	evidence := []string{
		fmt.Sprintf("container=%s", cs.Name),
		"state=running ready=false",
		fmt.Sprintf("restartCount=%d", cs.RestartCount),
	}
	for _, event := range events {
		if !pattern.ReadinessFailure(event.Reason, event.Message) {
			continue
		}
		evidence = append(evidence, fmt.Sprintf("event: %s", event.Message))
		return d.finding(
			resource,
			pod,
			cs,
			CauseReadinessProbeFailing,
			fmt.Sprintf("container %q in %s is running but not ready because its readiness probe is failing after the rollout exceeded its progress deadline", cs.Name, resource),
			evidence,
			false,
		)
	}

	evidence = append(evidence,
		"container is running and not ready after the rollout exceeded its progress deadline",
		"no Unhealthy event explaining the unready state was observed",
	)
	return d.finding(
		resource,
		pod,
		cs,
		CauseReadinessUndetermined,
		fmt.Sprintf("container %q in %s is running but not ready after the rollout exceeded its progress deadline, but no observed event identifies why", cs.Name, resource),
		evidence,
		true,
	)
}

func (Readiness) finding(resource model.ResourceRef, pod corev1.Pod, cs corev1.ContainerStatus, causeID CauseID, cause string, evidence []string, undetermined bool) model.Finding {
	return model.Finding{
		CheckID:  string(causeID),
		Severity: model.SeverityHigh,
		Cause:    cause,
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "a readiness probe pointing at the wrong path or port and an application that is genuinely not serving produce the same symptom; inspect the pod events and current container logs before changing either",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", pod.Name, pod.Namespace), fmt.Sprintf("kubectl logs %s -c %s -n %s", pod.Name, cs.Name, pod.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: undetermined,
	}
}
