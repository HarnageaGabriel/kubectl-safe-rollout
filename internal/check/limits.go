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

package check

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ResourceLimitsCheckID is the stable identifier for this check.
const ResourceLimitsCheckID = "resource-limits"

// ResourceLimits checks for CPU and memory limits in regular containers
// in the pod template.
type ResourceLimits struct{}

// ID implements check.Check.
func (ResourceLimits) ID() string { return ResourceLimitsCheckID }

// Run implements check.Check.
func (c ResourceLimits) Run(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, container := range target.Workload.PodContainers() {
		resource := model.ResourceRef{
			Kind:      "Pod",
			Namespace: target.Namespace,
			Name:      fmt.Sprintf("%s/%s", target.Workload.Name(), container.Name),
		}
		if _, ok := container.Resources.Limits[corev1.ResourceCPU]; !ok {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityLow,
				Cause:    fmt.Sprintf("container %q in the pod template of workload %q does not define a CPU limit", container.Name, target.Workload.Name()),
				Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("add a CPU limit to container %q to make throttling predictable and isolate its consumption from other workloads on the node; the value depends on the application's profile", container.Name),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
		if _, ok := container.Resources.Limits[corev1.ResourceMemory]; !ok {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityLow,
				Cause:    fmt.Sprintf("container %q in the pod template of workload %q does not define a memory limit", container.Name, target.Workload.Name()),
				Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("add a memory limit to container %q to contain consumption and prevent node-level OOMKills; the value depends on the application's profile", container.Name),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}
