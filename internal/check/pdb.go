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
	if replicas == 0 {
		// A workload scaled to zero has no running Pods for the Eviction API
		// to ever act on, so a matching PDB cannot currently block anything:
		// the Cause text below claims a concurrent drain "will remain
		// blocked", which is false when nothing is running. Found on kind:
		// this also produced a remediation suggesting "minAvailable: -1", a
		// value the API server would reject outright (replicas-1 with
		// replicas=0).
		return Result{CheckID: c.ID()}, nil
	}
	podLabels := labels.Set(target.Workload.PodLabels())
	strategy := target.Workload.UpdateStrategy()
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	var findings []model.Finding
	matchedPDB := false
	for _, pdb := range pdbList.Items {
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || selector.Empty() || !selector.Matches(podLabels) {
			continue
		}
		matchedPDB = true

		allowed, mode, err := allowedDisruptions(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable, replicas)
		if err != nil {
			// A PDB whose Spec lacks both MinAvailable and
			// MaxUnavailable is not a valid object from the API server's
			// perspective: this should not happen on a real cluster, but
			// if it does, reporting it is outside this check's scope.
			continue
		}

		resource := model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: pdb.Namespace, Name: pdb.Name}

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

	if !matchedPDB && replicas > 1 {
		// Severity Low, not High: unlike the findings above, this is not
		// a verified fact about a concrete blockage, it is the absence of
		// a safety net that popular offline linters (Popeye, kube-score)
		// already flag for the same reason. Gated on replicas > 1: a
		// single-replica workload has no disruption tolerance to budget
		// for regardless of whether a PDB exists.
		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: model.SeverityLow,
			Cause: fmt.Sprintf(
				"%s has %d replicas but no PodDisruptionBudget selects its pods: a node drain concurrent with the rollout could terminate more than one replica at once, with nothing to stop it",
				workloadRef, replicas,
			),
			Evidence: []string{fmt.Sprintf("replicas=%d", replicas), "matchingPodDisruptionBudgets=0"},
			Remediation: model.Remediation{
				Summary:          noPDBRemediationSummary(target.Workload.Kind(), workloadRef),
				ContextDependent: true,
			},
			Resource: model.ResourceRef{Kind: target.Workload.Kind(), Namespace: target.Namespace, Name: target.Workload.Name()},
		})
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// allowedDisruptions calculates how many pods the PDB allows to be
// unavailable at once, matching the in-tree PDB controller exactly:
// k8s.io/kubernetes@v1.36.1 pkg/controller/disruption/disruption.go, the
// GetPodDisruptionsAllowed path, calls GetScaledValueFromIntOrPercent with
// roundUp=true for BOTH minAvailable (line ~845) and maxUnavailable (line
// ~826). roundUp only changes the result for a percentage value (an
// integer value is unaffected either way): rounding maxUnavailable down,
// as this function used to do, understated disruptionsAllowed for any
// percentage that does not divide replicas evenly (for example
// maxUnavailable: 25% on 3 replicas: floor(0.75)=0 vs. the real
// ceil(0.75)=1), producing a false "leaves no disruption headroom"
// finding for a PDB the real controller considers perfectly healthy.
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
		v, err := intstr.GetScaledValueFromIntOrPercent(maxUnavailable, int(replicas), true)
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

// noPDBRemediationSummary produces the "add a PodDisruptionBudget" advice
// for a workload with no matching PDB. DaemonSet needs a different shape of
// advice than Deployment/StatefulSet: DaemonSet pods are not owned by
// anything with a /scale subresource (see pdb-daemonset-scale), so a PDB
// using maxUnavailable in any form, or minAvailable expressed as a
// percentage, leaves the in-tree disruption controller permanently unable
// to compute disruptionsAllowed for it (status.conditions shows
// DisruptionAllowed=False, Reason=SyncFailed, forever, not just until the
// next reconcile). Suggesting maxUnavailable here, the advice this check
// already gives for Deployment/StatefulSet, would recommend exactly the
// misconfiguration pdb-daemonset-scale exists to catch.
func noPDBRemediationSummary(kind, workloadRef string) string {
	if kind == "DaemonSet" {
		return fmt.Sprintf(
			"add a PodDisruptionBudget selecting %s's pods using an integer minAvailable (for example minAvailable: 1); DaemonSet pods have no /scale subresource, so maxUnavailable (in any form) or a percentage minAvailable would leave the PDB permanently unable to compute disruptionsAllowed (see pdb-daemonset-scale) — the right integer value depends on how many replicas can be lost in production",
			workloadRef,
		)
	}
	return fmt.Sprintf(
		"add a PodDisruptionBudget selecting %s's pods (for example maxUnavailable: 1) to bound how many replicas a concurrent drain can take at once; the right value depends on how many replicas can be lost in production",
		workloadRef,
	)
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
