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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// QuotaDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const QuotaDiagnoserID = "quota"

// Quota classifies pod creation failures blocked by the ResourceQuota
// admission plugin. It is the only Diagnoser that does not operate on the
// workload's Pods: when quota blocks creation, the Pod never exists, so
// there is no Pod to inspect. The signal is on the ReplicaSet's
// Reason=="FailedCreate" event.
type Quota struct{}

// ID implements Diagnoser.
func (Quota) ID() string { return QuotaDiagnoserID }

// Diagnose implements Diagnoser.
func (d Quota) Diagnose(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, rs := range target.ReplicaSets {
		resource := model.ResourceRef{Kind: "ReplicaSet", Namespace: rs.Namespace, Name: rs.Name}
		for _, e := range target.EventsByUID[rs.UID] {
			if e.Reason != "FailedCreate" {
				continue
			}
			evidence := []string{fmt.Sprintf("event: %s", e.Message)}
			if pattern.QuotaExceeded(e.Message) {
				findings = append(findings, model.Finding{
					CheckID:  string(CauseQuotaExceeded),
					Severity: model.SeverityHigh,
					Cause:    fmt.Sprintf("%s cannot create pods: the namespace ResourceQuota is exhausted", resource),
					Evidence: evidence,
					Remediation: model.Remediation{
						Summary:          "increase the namespace ResourceQuota, reduce workload requests, or free capacity by terminating other workloads; the correct choice depends on what the namespace hosts and who has authority over it",
						Commands:         []string{fmt.Sprintf("kubectl describe resourcequota -n %s", rs.Namespace)},
						ContextDependent: true,
					},
					Resource: resource,
				})
				continue
			}
			findings = append(findings, model.Finding{
				CheckID:  string(CauseQuotaUndetermined),
				Severity: model.SeverityHigh,
				Cause:    fmt.Sprintf("%s cannot create pods, but the event message does not mention an exceeded ResourceQuota", resource),
				Evidence: evidence,
				Remediation: model.Remediation{
					Summary:          "read the full FailedCreate event message: it may be an admission webhook or another constraint, not necessarily a quota",
					Commands:         []string{fmt.Sprintf("kubectl describe replicaset %s -n %s", rs.Name, rs.Namespace)},
					ContextDependent: true,
				},
				Resource:     resource,
				Undetermined: true,
			})
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}
