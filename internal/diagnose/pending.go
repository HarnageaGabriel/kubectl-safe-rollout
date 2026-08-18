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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// PendingDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const PendingDiagnoserID = "pending"

// Pending classifies pods stuck in the Pending phase, distinguishing
// insufficient resources, scheduling constraints, and unbound
// PersistentVolumeClaims.
//
// Deliberate precedence order: the PVC referenced by the pod is checked
// first by reading its Status.Phase directly (structured signal, no text
// parsing), before inspecting the FailedScheduling event. Only if the PVC
// cannot be read or does not explain the Pending state does classification
// move to the event's text pattern.
type Pending struct{}

// ID implements Diagnoser.
func (Pending) ID() string { return PendingDiagnoserID }

// Diagnose implements Diagnoser.
func (d Pending) Diagnose(ctx context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		if pod.Status.Phase != corev1.PodPending {
			continue
		}
		if finding, ok := d.classify(ctx, target, pod); ok {
			findings = append(findings, finding)
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func (d Pending) classify(ctx context.Context, target Target, pod corev1.Pod) (model.Finding, bool) {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}

	if f, ok := d.classifyUnboundPVC(ctx, target, pod, resource); ok {
		return f, true
	}

	for _, e := range target.EventsByUID[pod.UID] {
		if e.Reason != "FailedScheduling" {
			continue
		}
		cause, ok := pattern.FailedScheduling(e.Message)
		if !ok {
			return d.undetermined(resource, []string{fmt.Sprintf("unrecognized FailedScheduling event: %s", e.Message)}), true
		}
		evidence := []string{fmt.Sprintf("event: %s", e.Message)}
		return d.findingForCause(cause, resource, evidence), true
	}

	// Pending without FailedScheduling and without an unbound PVC is normal
	// during startup/scheduling. It must not stop watch immediately.
	return model.Finding{}, false
}

// classifyUnboundPVC directly reads the Status.Phase of every
// PersistentVolumeClaim referenced by the pod: it is a structured field,
// not a free-text pattern, so it takes precedence over everything else. If
// reading a PVC fails (insufficient RBAC or otherwise), this function does
// not treat it as evidence of anything and lets classify() continue with
// the FailedScheduling event: a narrow read error on one PVC must not block
// classification of the entire pod.
func (Pending) classifyUnboundPVC(ctx context.Context, target Target, pod corev1.Pod, resource model.ResourceRef) (model.Finding, bool) {
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		claimName := vol.PersistentVolumeClaim.ClaimName
		pvc, err := target.Client.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, claimName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if pvc.Status.Phase == corev1.ClaimBound {
			continue
		}
		evidence := []string{
			fmt.Sprintf("volume=%s claim=%s", vol.Name, claimName),
			fmt.Sprintf("pvcPhase=%s", pvc.Status.Phase),
		}
		return model.Finding{
			CheckID:  string(CausePendingUnboundPVC),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"%s remains Pending because PersistentVolumeClaim %q is not Bound (current phase: %s)",
				resource, claimName, pvc.Status.Phase,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"check why %q is not binding to a PersistentVolume: its storageClass may have no available provisioner, capacity may be insufficient, or zone/node constraints may be incompatible with where the pod can be scheduled",
					claimName,
				),
				ContextDependent: true,
			},
			Resource: resource,
		}, true
	}
	return model.Finding{}, false
}

func (Pending) findingForCause(cause string, resource model.ResourceRef, evidence []string) model.Finding {
	switch cause {
	case "insufficient-resources":
		return model.Finding{
			CheckID:  string(CausePendingInsufficientResources),
			Severity: model.SeverityHigh,
			Cause:    fmt.Sprintf("%s remains Pending: no node has enough CPU, memory, or ephemeral storage for scheduling", resource),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          "reduce pod requests if oversized, or add cluster capacity (new nodes or cluster-autoscaler); the correct value depends on the actual expected load",
				ContextDependent: true,
			},
			Resource: resource,
		}
	case "scheduling-constraints":
		return model.Finding{
			CheckID:  string(CausePendingSchedulingConstraints),
			Severity: model.SeverityHigh,
			Cause:    fmt.Sprintf("%s remains Pending: nodeSelector, affinity, or taint/toleration excludes all available nodes", resource),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          "check the pod's nodeSelector/affinity against available node labels and its tolerations against taints: the correct constraint depends on where the workload must actually run",
				ContextDependent: true,
			},
			Resource: resource,
		}
	case "unbound-pvc":
		// The scheduler reports a PVC issue that the structured read in
		// classifyUnboundPVC did not confirm (e.g. insufficient RBAC on the
		// PVC): the signal remains valid without direct confirmation and
		// must not be discarded.
		return model.Finding{
			CheckID:  string(CausePendingUnboundPVC),
			Severity: model.SeverityHigh,
			Cause:    fmt.Sprintf("%s remains Pending: the scheduler reports an unbound PersistentVolumeClaim", resource),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          "check the status of the PersistentVolumeClaims referenced by the pod",
				Commands:         []string{fmt.Sprintf("kubectl get pvc -n %s", resource.Namespace)},
				ContextDependent: true,
			},
			Resource: resource,
		}
	default:
		return Pending{}.undetermined(resource, evidence)
	}
}

func (Pending) undetermined(resource model.ResourceRef, evidence []string) model.Finding {
	return model.Finding{
		CheckID:  string(CausePendingUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s remains Pending but the collected evidence does not distinguish among insufficient resources, scheduling constraints, and an unbound PersistentVolumeClaim", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "collect more context: describe the pod to see the full scheduling event messages",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", resource.Name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
}
