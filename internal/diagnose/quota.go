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

// Quota classifies pod creation failures reported through the ReplicaSet's
// Reason=="FailedCreate" event: quota exhaustion is the most common cause,
// but not the only one an admission plugin can produce there, so this
// Diagnoser also recognizes a missing ServiceAccount rather than leaving it
// in the undetermined bucket alongside genuinely unrecognized rejections. It
// is the only Diagnoser that does not operate on the workload's Pods: when
// pod creation itself is rejected, the Pod never exists, so there is no Pod
// to inspect.
type Quota struct{}

// ID implements Diagnoser.
func (Quota) ID() string { return QuotaDiagnoserID }

// maxQuotaEvidence bounds how many FailedCreate messages a single Finding
// prints. The ReplicaSet controller keeps retrying for as long as the quota
// stays exhausted, so the event list grows without bound while every entry
// says the same thing apart from the generated pod name. Three is enough to
// show the shape of the rejection and which quota it names; the rest are
// reported as a count rather than dropped silently, because output that
// looks complete while hiding what it removed is the failure mode this
// project refuses everywhere else.
const maxQuotaEvidence = 3

// capEvidence truncates to maxQuotaEvidence entries, appending a line that
// states how many were omitted.
func capEvidence(evidence []string) []string {
	if len(evidence) <= maxQuotaEvidence {
		return evidence
	}
	capped := append([]string(nil), evidence[:maxQuotaEvidence]...)
	return append(capped, fmt.Sprintf("... and %d more rejections with the same cause", len(evidence)-maxQuotaEvidence))
}

// Diagnose implements Diagnoser.
//
// One rejected pod creation produces one FailedCreate event, so a rollout
// of N replicas blocked by quota produces N events describing a single
// failure. They are collapsed into one Finding per cause with the
// individual messages kept as separate Evidence lines: the per-pod
// messages are worth reading (they name which pods were rejected) but
// printing the same cause and the same remediation N times is noise
// exactly where the output is read under pressure. This grouping is not
// generalised to the other Diagnosers, which iterate over Pods: there,
// two Findings mean two genuinely different objects failing.
func (d Quota) Diagnose(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, rs := range target.ReplicaSets {
		resource := model.ResourceRef{Kind: "ReplicaSet", Namespace: rs.Namespace, Name: rs.Name}

		var exceeded, undetermined []string
		var missingSA string
		var missingSAEvidence []string
		for _, e := range target.EventsByUID[rs.UID] {
			if e.Reason != "FailedCreate" {
				continue
			}
			message := fmt.Sprintf("event: %s", e.Message)
			switch name, ok := pattern.ServiceAccountMissing(e.Message); {
			case pattern.QuotaExceeded(e.Message):
				exceeded = append(exceeded, message)
			case ok:
				missingSA = name
				missingSAEvidence = append(missingSAEvidence, message)
			default:
				undetermined = append(undetermined, message)
			}
		}

		if len(exceeded) > 0 {
			findings = append(findings, model.Finding{
				CheckID:  string(CauseQuotaExceeded),
				Severity: model.SeverityHigh,
				Cause:    fmt.Sprintf("%s cannot create pods: the namespace ResourceQuota is exhausted", resource),
				Evidence: capEvidence(exceeded),
				Remediation: model.Remediation{
					Summary:          "increase the namespace ResourceQuota, reduce workload requests, or free capacity by terminating other workloads; the correct choice depends on what the namespace hosts and who has authority over it",
					Commands:         []string{fmt.Sprintf("kubectl describe resourcequota -n %s", rs.Namespace)},
					ContextDependent: true,
				},
				Resource: resource,
			})
		}

		if len(missingSAEvidence) > 0 {
			findings = append(findings, model.Finding{
				CheckID:  string(CauseServiceAccountMissing),
				Severity: model.SeverityHigh,
				Cause:    fmt.Sprintf("%s cannot create pods: the pod template references ServiceAccount %q, which does not exist in namespace %q", resource, missingSA, rs.Namespace),
				Evidence: capEvidence(missingSAEvidence),
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("verify whether ServiceAccount %q should exist in namespace %q, or the pod template should reference a different one", missingSA, rs.Namespace),
					Commands:         []string{fmt.Sprintf("kubectl get serviceaccount %s -n %s", missingSA, rs.Namespace)},
					ContextDependent: true,
				},
				Resource: resource,
			})
		}

		if len(undetermined) > 0 {
			findings = append(findings, model.Finding{
				CheckID:  string(CauseQuotaUndetermined),
				Severity: model.SeverityHigh,
				Cause:    fmt.Sprintf("%s cannot create pods, but the event message does not mention an exceeded ResourceQuota or a missing ServiceAccount", resource),
				Evidence: capEvidence(undetermined),
				Remediation: model.Remediation{
					Summary:          "read the full FailedCreate event message: it may be an admission webhook or another constraint",
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
