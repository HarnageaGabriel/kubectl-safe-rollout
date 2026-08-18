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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// PDBCheckID is the stable identifier for this check, used in JSON output
// and for CI gating on individual rules.
const PDBCheckID = "pdb-consistency"

// PDBConsistency checks that PodDisruptionBudgets covering a workload
// actually leave disruption headroom.
//
// Deliberate technical note: a PDB is enforced by the Eviction API (used
// by `kubectl drain`, the cluster-autoscaler, and the descheduler), not by
// the Deployment controller when it replaces pods during a rolling update.
// Therefore, this check does not claim that the PDB itself blocks
// `kubectl rollout`: it states that if a node drain or maintenance occurs
// during the rollout window, it remains blocked until the budget frees up.
// This is the scenario described in the project brief and the most common
// real cause of rollouts that "appear" stuck.
type PDBConsistency struct{}

// ID implements check.Check.
func (PDBConsistency) ID() string { return PDBCheckID }

// Run implements check.Check.
func (c PDBConsistency) Run(ctx context.Context, target Target) (Result, error) {
	pdbList, err := target.Client.PolicyV1().PodDisruptionBudgets(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("PodDisruptionBudget list is not accessible: %v", err)), nil
	}

	replicas := target.Workload.Replicas()
	podLabels := labels.Set(target.Workload.PodLabels())
	strategy := target.Workload.UpdateStrategy()

	var findings []model.Finding
	for _, pdb := range pdbList.Items {
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || selector.Empty() || !selector.Matches(podLabels) {
			continue
		}

		allowed, mode, err := allowedDisruptions(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable, replicas)
		if err != nil {
			// A PDB whose Spec lacks both MinAvailable and
			// MaxUnavailable is not a valid object from the API server's
			// perspective: this should not happen on a real cluster, but
			// if it does, reporting it is outside this check's scope.
			continue
		}

		resource := model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: pdb.Namespace, Name: pdb.Name}
		workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

		if allowed <= 0 {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityHigh,
				Cause: fmt.Sprintf(
					"PodDisruptionBudget %q leaves no disruption headroom (%s calculated over %d replicas): a node drain concurrent with the rollout of %s will remain blocked until the pods are ready again",
					pdb.Name, mode, replicas, workloadRef,
				),
				Evidence: []string{
					fmt.Sprintf("%s=%s", mode, pdbSpecValue(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable)),
					fmt.Sprintf("replicas=%d", replicas),
					fmt.Sprintf("disruptionsAllowed=%d", allowed),
				},
				Remediation: model.Remediation{
					Summary: fmt.Sprintf(
						"increase the PDB headroom (for example, maxUnavailable: 1 or minAvailable: %d) or increase the replicas of %s; the correct value depends on how many replicas can be lost in production",
						replicas-1, workloadRef,
					),
					ContextDependent: true,
				},
				Resource: resource,
			})
			continue
		}

		if strategy.Type == workload.Recreate && allowed < replicas {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityHigh,
				Cause: fmt.Sprintf(
					"%s uses the Recreate strategy (all pods are terminated together), but PodDisruptionBudget %q allows only %d disruptions out of %d replicas: node maintenance during the rollout would still be blocked",
					workloadRef, pdb.Name, allowed, replicas,
				),
				Evidence: []string{
					"strategy=Recreate",
					fmt.Sprintf("%s=%s", mode, pdbSpecValue(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable)),
					fmt.Sprintf("disruptionsAllowed=%d", allowed),
				},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("switch %s to the RollingUpdate strategy, or widen the PDB to maxUnavailable equal to the total replicas if Recreate is a deliberate choice", workloadRef),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// allowedDisruptions calculates how many pods the PDB allows to be
// unavailable at once. roundUp reflects the behavior of the in-tree PDB
// controller: minAvailable rounds up (more conservative about the number
// of pods that must remain available), while maxUnavailable rounds down
// (more conservative about the number of allowed disruptions).
func allowedDisruptions(minAvailable, maxUnavailable *intstr.IntOrString, replicas int32) (allowed int32, mode string, err error) {
	switch {
	case minAvailable != nil:
		v, err := intstr.GetScaledValueFromIntOrPercent(minAvailable, int(replicas), true)
		if err != nil {
			return 0, "", err
		}
		allowed = replicas - int32(v)
		mode = "minAvailable"
	case maxUnavailable != nil:
		v, err := intstr.GetScaledValueFromIntOrPercent(maxUnavailable, int(replicas), false)
		if err != nil {
			return 0, "", err
		}
		allowed = int32(v)
		mode = "maxUnavailable"
	default:
		return 0, "", fmt.Errorf("PDB has neither minAvailable nor maxUnavailable")
	}
	if allowed < 0 {
		allowed = 0
	}
	return allowed, mode, nil
}

func pdbSpecValue(minAvailable, maxUnavailable *intstr.IntOrString) string {
	if minAvailable != nil {
		return minAvailable.String()
	}
	if maxUnavailable != nil {
		return maxUnavailable.String()
	}
	return "<unset>"
}
