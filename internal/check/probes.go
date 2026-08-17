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

// ProbeSanityCheckID e' l'identificativo stabile di questa verifica.
const ProbeSanityCheckID = "probe-sanity"

// ProbeSanity verifica la presenza delle probe di readiness e liveness
// nei container regolari del pod template. Non valuta soglie o tempi:
// senza il tempo di startup reale dell'app sarebbero euristiche arbitrarie.
type ProbeSanity struct{}

// ID implementa check.Check.
func (ProbeSanity) ID() string { return ProbeSanityCheckID }

// Run implementa check.Check.
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
				Cause:    fmt.Sprintf("il container %q nel pod template del workload %q non definisce una readinessProbe", container.Name, target.Workload.Name()),
				Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("aggiungi una readinessProbe al container %q usando un controllo che rappresenti quando l'app puo' ricevere traffico", container.Name),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
		if container.LivenessProbe == nil {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityLow,
				Cause:    fmt.Sprintf("il container %q nel pod template del workload %q non definisce una livenessProbe", container.Name, target.Workload.Name()),
				Evidence: []string{fmt.Sprintf("container=%s", container.Name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("aggiungi una livenessProbe al container %q usando un controllo che rilevi quando l'app deve essere riavviata", container.Name),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}
