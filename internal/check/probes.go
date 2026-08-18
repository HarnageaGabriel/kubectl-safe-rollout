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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ProbeSanityCheckID is the stable identifier for this check.
const ProbeSanityCheckID = "probe-sanity"

// ProbeSanity checks for readiness and liveness probes in regular containers
// in the pod template. It does not evaluate thresholds or timings: without
// the application's actual startup time, such heuristics would be arbitrary.
type ProbeSanity struct{}

// ID implements check.Check.
func (ProbeSanity) ID() string { return ProbeSanityCheckID }

// Run implements check.Check.
func (c ProbeSanity) Run(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, container := range target.Workload.PodContainers() {
		resource := model.ResourceRef{
			Kind:      "Pod",
			Namespace: target.Namespace,
			Name:      fmt.Sprintf("%s/%s", target.Workload.Name(), container.Name),
		}
		if container.ReadinessProbe == nil {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityLow,
				Cause:    fmt.Sprintf("container %q in the pod template of workload %q does not define a readinessProbe", container.Name, target.Workload.Name()),
				Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("add a readinessProbe to container %q using a check that represents when the application can receive traffic", container.Name),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
		if container.LivenessProbe == nil {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityLow,
				Cause:    fmt.Sprintf("container %q in the pod template of workload %q does not define a livenessProbe", container.Name, target.Workload.Name()),
				Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("add a livenessProbe to container %q using a check that detects when the application must be restarted", container.Name),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}
