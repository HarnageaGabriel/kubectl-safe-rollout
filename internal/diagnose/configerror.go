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

// ConfigErrorDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const ConfigErrorDiagnoserID = "container-config"

// ConfigError classifies containers whose configuration cannot be built
// because a referenced ConfigMap or Secret is missing.
type ConfigError struct{}

// ID implements Diagnoser.
func (ConfigError) ID() string { return ConfigErrorDiagnoserID }

// Diagnose implements Diagnoser.
func (d ConfigError) Diagnose(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		statuses := make([]corev1.ContainerStatus, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
		statuses = append(statuses, pod.Status.InitContainerStatuses...)
		statuses = append(statuses, pod.Status.ContainerStatuses...)
		for _, cs := range statuses {
			if cs.State.Waiting == nil || cs.State.Waiting.Reason != "CreateContainerConfigError" {
				continue
			}
			findings = append(findings, d.classify(pod, cs, target.EventsByUID[pod.UID]))
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func (d ConfigError) classify(pod corev1.Pod, cs corev1.ContainerStatus, events []corev1.Event) model.Finding {
	waiting := cs.State.Waiting
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	evidence := []string{
		fmt.Sprintf("container=%s", cs.Name),
		fmt.Sprintf("waitingReason=%s", waiting.Reason),
		fmt.Sprintf("waitingMessage=%s", waiting.Message),
	}
	kind, name, ok := pattern.MissingConfigObject(waiting.Message)
	for _, e := range events {
		if e.Reason != "Failed" || !matchingConfigErrorEvent(e.Message, waiting.Message, kind, name, ok) {
			continue
		}
		evidence = append(evidence, fmt.Sprintf("event: %s", e.Message))
		break
	}
	if !ok {
		return d.undetermined(resource, evidence)
	}
	return d.missingObject(resource, cs.Name, kind, name, evidence)
}

func matchingConfigErrorEvent(eventMessage, waitingMessage, kind, name string, parsed bool) bool {
	if !parsed {
		return waitingMessage != "" && strings.Contains(eventMessage, waitingMessage)
	}
	eventKind, eventName, ok := pattern.MissingConfigObject(eventMessage)
	return ok && eventKind == kind && eventName == name
}

func (ConfigError) missingObject(resource model.ResourceRef, container, kind, name string, evidence []string) model.Finding {
	causeID := CauseConfigErrorMissingSecret
	displayKind := "Secret"
	if kind == "configmap" {
		causeID = CauseConfigErrorMissingConfigMap
		displayKind = "ConfigMap"
	}
	return model.Finding{
		CheckID:  string(causeID),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"container %q in %s cannot start because referenced %s %q does not exist",
			container, resource, displayKind, name,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"verify whether %s %q should be created or the container reference should name a different object",
				displayKind, name,
			),
			Commands:         []string{fmt.Sprintf("kubectl get %s %s -n %s", kind, name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource: resource,
	}
}

func (ConfigError) undetermined(resource model.ResourceRef, evidence []string) model.Finding {
	return model.Finding{
		CheckID:  string(CauseConfigErrorUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s cannot start because its container configuration is invalid, but the waiting message names no recognized missing ConfigMap or Secret", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "inspect the pod events and container references to identify the invalid configuration",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", resource.Name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
}
