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

// ResourceLimitsCheckID e' l'identificativo stabile di questa verifica.
const ResourceLimitsCheckID = "resource-limits"

// ResourceLimits verifica la presenza dei limiti CPU e memoria nei
// container regolari del pod template.
type ResourceLimits struct{}

// ID implementa check.Check.
func (ResourceLimits) ID() string { return ResourceLimitsCheckID }

// Run implementa check.Check.
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
				Cause:    fmt.Sprintf("il container %q nel pod template del workload %q non definisce un limite CPU", container.Name, target.Workload.Name()),
				Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("aggiungi un limite CPU al container %q per rendere prevedibile il throttling e isolarne il consumo dagli altri workload sul nodo; il valore dipende dal profilo dell'app", container.Name),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
		if _, ok := container.Resources.Limits[corev1.ResourceMemory]; !ok {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityLow,
				Cause:    fmt.Sprintf("il container %q nel pod template del workload %q non definisce un limite di memoria", container.Name, target.Workload.Name()),
				Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("aggiungi un limite di memoria al container %q per contenere il consumo ed evitare OOMKill a livello di nodo; il valore dipende dal profilo dell'app", container.Name),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}
