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

// VolumeMountDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const VolumeMountDiagnoserID = "volume-mount"

// VolumeMount classifies FailedMount events that keep a Pod's containers
// from running.
type VolumeMount struct{}

// ID implements Diagnoser.
func (VolumeMount) ID() string { return VolumeMountDiagnoserID }

// Diagnose implements Diagnoser.
func (d VolumeMount) Diagnose(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		if !hasNonRunningContainer(pod) {
			continue
		}
		// ContainerCreating carries no volume failure details. If the
		// FailedMount event has aged out, the cause is no longer observable;
		// reporting nothing is safer than guessing from a stuck container.
		if finding, ok := d.classify(pod, target.EventsByUID[pod.UID]); ok {
			findings = append(findings, finding)
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func hasNonRunningContainer(pod corev1.Pod) bool {
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Running == nil {
			return true
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Running == nil {
			return true
		}
	}
	return false
}

func (d VolumeMount) classify(pod corev1.Pod, events []corev1.Event) (model.Finding, bool) {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	var unrecognizedEvent *corev1.Event
	for i := range events {
		event := &events[i]
		if event.Reason != "FailedMount" {
			continue
		}
		kind, name, ok := pattern.MissingConfigObject(event.Message)
		if ok {
			evidence := []string{fmt.Sprintf("event: %s", event.Message)}
			return d.missingObject(resource, kind, name, evidence), true
		}
		if unrecognizedEvent == nil {
			unrecognizedEvent = event
		}
	}
	if unrecognizedEvent != nil {
		evidence := []string{fmt.Sprintf("event: %s", unrecognizedEvent.Message)}
		return d.undetermined(resource, evidence), true
	}
	return model.Finding{}, false
}

func (VolumeMount) missingObject(resource model.ResourceRef, kind, name string, evidence []string) model.Finding {
	causeID := CauseVolumeMountMissingSecret
	displayKind := "Secret"
	if kind == "configmap" {
		causeID = CauseVolumeMountMissingConfigMap
		displayKind = "ConfigMap"
	}
	return model.Finding{
		CheckID:  string(causeID),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s cannot mount a volume because referenced %s %q does not exist", resource, displayKind, name),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"verify whether %s %q should be created or the volume reference should name a different object",
				displayKind, name,
			),
			Commands:         []string{fmt.Sprintf("kubectl get %s %s -n %s", kind, name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource: resource,
	}
}

func (VolumeMount) undetermined(resource model.ResourceRef, evidence []string) model.Finding {
	return model.Finding{
		CheckID:  string(CauseVolumeMountUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s cannot mount a volume, but the FailedMount event names no recognized missing Secret or ConfigMap", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "inspect the full FailedMount event and the pod's volume references before changing the workload",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", resource.Name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
}
