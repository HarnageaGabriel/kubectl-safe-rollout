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

// ResourceLimits checks for CPU and memory limits in every container in the
// pod template, regular and init. The two are not a stylistic split: the
// kubelet enforces an init container's cgroup limits exactly like a regular
// one's, so an init container without a memory limit can OOM the node the
// same way, and this check would otherwise miss it entirely (found on kind:
// a Deployment with a fully-limited main container and a limit-less init
// container reported clean).
type ResourceLimits struct{}

// ID implements check.Check.
func (ResourceLimits) ID() string { return ResourceLimitsCheckID }

// Run implements check.Check.
func (c ResourceLimits) Run(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, container := range target.Workload.PodContainers() {
		findings = append(findings, c.containerFindings(target, container, "container")...)
	}
	for _, container := range target.Workload.InitContainers() {
		findings = append(findings, c.containerFindings(target, container, "init container")...)
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// containerFindings checks one container for missing CPU and memory limits.
// kind distinguishes a regular container from an init container in the
// finding text, since the fix and the risk it names ("throttling other
// workloads" vs. an OOM during startup) read differently for each.
func (c ResourceLimits) containerFindings(target Target, container corev1.Container, kind string) []model.Finding {
	var findings []model.Finding
	resource := model.ResourceRef{
		Kind:      "Pod",
		Namespace: target.Namespace,
		Name:      fmt.Sprintf("%s/%s", target.Workload.Name(), container.Name),
	}
	if _, ok := container.Resources.Limits[corev1.ResourceCPU]; !ok {
		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: model.SeverityLow,
			Cause:    fmt.Sprintf("%s %q in the pod template of workload %q does not define a CPU limit", kind, container.Name, target.Workload.Name()),
			Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("add a CPU limit to %s %q to make throttling predictable and isolate its consumption from other workloads on the node; the value depends on the application's profile", kind, container.Name),
				ContextDependent: true,
			},
			Resource: resource,
		})
	}
	if _, ok := container.Resources.Limits[corev1.ResourceMemory]; !ok {
		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: model.SeverityLow,
			Cause:    fmt.Sprintf("%s %q in the pod template of workload %q does not define a memory limit", kind, container.Name, target.Workload.Name()),
			Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("add a memory limit to %s %q to contain consumption and prevent node-level OOMKills; the value depends on the application's profile", kind, container.Name),
				ContextDependent: true,
			},
			Resource: resource,
		})
	}
	return findings
}
