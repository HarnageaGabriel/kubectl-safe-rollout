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

// ImagePullDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const ImagePullDiagnoserID = "imagepull"

// ImagePull classifies containers in ImagePullBackOff/ErrImagePull,
// distinguishing a missing tag/digest, an unreachable registry, and
// missing credentials. The structured signal (ContainerState.Waiting.
// Reason) only identifies that the pull fails; the reason is in the text
// of the associated Reason=="Failed" event, isolated in
// internal/diagnose/pattern.
type ImagePull struct{}

// ID implements Diagnoser.
func (ImagePull) ID() string { return ImagePullDiagnoserID }

// Diagnose implements Diagnoser.
func (d ImagePull) Diagnose(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting == nil {
				continue
			}
			reason := cs.State.Waiting.Reason
			if reason != "ImagePullBackOff" && reason != "ErrImagePull" {
				continue
			}
			findings = append(findings, d.classify(pod, cs, target.EventsByUID[pod.UID]))
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func (d ImagePull) classify(pod corev1.Pod, cs corev1.ContainerStatus, events []corev1.Event) model.Finding {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	base := []string{
		fmt.Sprintf("container=%s", cs.Name),
		fmt.Sprintf("image=%s", cs.Image),
		fmt.Sprintf("waitingReason=%s", cs.State.Waiting.Reason),
	}

	for _, e := range events {
		if e.Reason != "Failed" {
			continue
		}
		cause, ok := pattern.ImagePullFailure(e.Message)
		if !ok {
			continue
		}
		evidence := append(append([]string(nil), base...), fmt.Sprintf("event: %s", e.Message))
		return d.findingForCause(cause, pod, cs, resource, evidence)
	}

	return d.undetermined(resource, base)
}

func (ImagePull) findingForCause(cause string, pod corev1.Pod, cs corev1.ContainerStatus, resource model.ResourceRef, evidence []string) model.Finding {
	image := cs.Image
	switch cause {
	case "tag-not-found":
		return model.Finding{
			CheckID:  string(CauseImagePullTagNotFound),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"container %q in %s cannot pull image %q: tag or digest does not exist in the registry",
				cs.Name, resource, image,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"verify that %q exists with that tag/digest in the registry: the tag may contain a typo, or the build may not have been published yet",
					image,
				),
				ContextDependent: true,
			},
			Resource: resource,
		}
	case "unauthorized":
		return model.Finding{
			CheckID:  string(CauseImagePullUnauthorized),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"container %q in %s cannot pull image %q: the registry rejects the pull because authentication or authorization is missing",
				cs.Name, resource, image,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("add or renew imagePullSecrets on the ServiceAccount or Pod for the registry hosting %q", image),
				Commands:         []string{fmt.Sprintf("kubectl get pod %s -n %s -o jsonpath={.spec.imagePullSecrets}", pod.Name, pod.Namespace)},
				ContextDependent: true,
			},
			Resource: resource,
		}
	case "registry-unreachable":
		return model.Finding{
			CheckID:  string(CauseImagePullRegistryUnreachable),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"container %q in %s cannot pull image %q: the registry is unreachable from the cluster (DNS, network, or timeout)",
				cs.Name, resource, image,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("verify from the node that the registry hosting %q is reachable (DNS, network policy, proxy), or check the registry itself", image),
				ContextDependent: true,
			},
			Resource: resource,
		}
	default:
		// This should not happen: pattern.ImagePullFailure returns only
		// these three strings when ok=true. If it does, it is an internal
		// alignment bug between the two packages, not a cluster condition:
		// return undetermined instead of a finding with an invented CheckID.
		return ImagePull{}.undetermined(resource, evidence)
	}
}

func (ImagePull) undetermined(resource model.ResourceRef, evidence []string) model.Finding {
	return model.Finding{
		CheckID:  string(CauseImagePullUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s cannot pull the image but no recognized event explains why", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "collect more context: describe the pod to see the full pull event message",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", resource.Name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
}
