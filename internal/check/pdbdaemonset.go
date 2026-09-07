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

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// PDBDaemonsetScaleCheckID is the stable identifier for this check.
const PDBDaemonsetScaleCheckID = "pdb-daemonset-scale"

// PDBDaemonsetScale checks a mechanism distinct from pdb-consistency: a
// PodDisruptionBudget whose selector matches a DaemonSet's pods, expressed
// with maxUnavailable (in any form, integer or percentage) or with a
// percentage minAvailable, permanently breaks the in-tree disruption
// controller's ability to compute disruptionsAllowed for that PDB.
//
// Verified against k8s.io/kubernetes@v1.36.1/pkg/controller/disruption/
// disruption.go: getExpectedPodCount (lines 813-853) calls getExpectedScale
// (which calls getScaleController, lines 372+) whenever
// pdb.Spec.MaxUnavailable is set (any form) or pdb.Spec.MinAvailable is a
// percentage — never when MinAvailable is an integer, which computes
// desiredHealthy directly from pdb.Spec.MinAvailable.IntVal without ever
// looking up a controller's scale. getExpectedScale resolves, for each pod
// under the PDB, the owning controller's declared scale by trying a fixed
// list of finders (dc.finders(), line 268-271:
// ReplicationController, Deployment, ReplicaSet, StatefulSet, then a
// generic getScaleController that reads the /scale subresource). There is
// no DaemonSet-specific finder, and DaemonSet declares no
// "+k8s:supportsSubresource=/scale" marker at all
// (k8s.io/api@v0.37.0/apps/v1/types.go, line 815-816, contrast with
// ReplicaSet's markers at line 397-398): the generic finder's /scale read
// fails for a DaemonSet-owned pod, getExpectedScale returns an error, and
// the whole PDB sync calls failSafe (disruption.go, line 978), which
// unconditionally sets status.disruptionsAllowed=0 and appends a
// status.conditions entry with Type=DisruptionAllowed (policy/v1's
// DisruptionAllowedCondition constant), Status=False, Reason=SyncFailed
// (policy/v1's SyncFailedReason constant) — every single reconcile, not a
// transient error that clears on retry, because the underlying mechanism
// (no scale subresource for DaemonSet) never changes.
//
// A PDB with an integer minAvailable never enters that code path at all
// (confirmed at disruption.go lines 834-837): it is a legitimate, fully
// functional configuration for a DaemonSet-selecting PDB and produces no
// finding here.
type PDBDaemonsetScale struct{}

// ID implements check.Check.
func (PDBDaemonsetScale) ID() string { return PDBDaemonsetScaleCheckID }

// Run implements check.Check.
func (c PDBDaemonsetScale) Run(ctx context.Context, target Target) (Result, error) {
	if target.Workload.Kind() != "DaemonSet" {
		return Result{CheckID: c.ID()}, nil
	}

	desired := target.Workload.Replicas()
	if desired == 0 {
		// No node is currently eligible to run this DaemonSet's pods:
		// getPodsForPdb would return zero pods, the loop inside
		// getExpectedScale never runs, and no error is ever produced
		// regardless of the PDB's spec shape.
		return Result{CheckID: c.ID()}, nil
	}

	pdbList, err := target.Client.PolicyV1().PodDisruptionBudgets(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("PodDisruptionBudget list is not accessible: %v", err)), nil
	}

	podLabels := labels.Set(target.Workload.PodLabels())
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	var findings []model.Finding
	for _, pdb := range pdbList.Items {
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || selector.Empty() || !selector.Matches(podLabels) {
			continue
		}

		mode, specValue, unverifiable := pdbDaemonsetUnverifiableForm(pdb.Spec)
		if !unverifiable {
			continue
		}

		severity, conditionEvidence := pdbDaemonsetSeverity(pdb)
		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: severity,
			Cause: fmt.Sprintf(
				"PodDisruptionBudget %q selects %s's pods and sets %s=%s, but DaemonSet pods have no /scale subresource: the disruption controller cannot compute the owning controller's scale for them, so it fails the sync and permanently reports disruptionsAllowed=0 with condition DisruptionAllowed=False/SyncFailed — an integer minAvailable is the only form this controller can evaluate for a DaemonSet-selecting PDB",
				pdb.Name, workloadRef, mode, specValue,
			),
			Evidence: append([]string{
				fmt.Sprintf("%s=%s", mode, specValue),
				fmt.Sprintf("desiredNumberScheduled=%d", desired),
			}, conditionEvidence...),
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"the only form of PodDisruptionBudget %q's spec this controller can evaluate for a DaemonSet is an integer minAvailable (e.g. minAvailable: 1); maxUnavailable in any form, and minAvailable expressed as a percentage, are not evaluable for DaemonSet pods, which have no /scale subresource",
					pdb.Name,
				),
				Commands: []string{
					fmt.Sprintf("kubectl get pdb %s -n %s -o jsonpath='{.status.conditions}'", pdb.Name, target.Namespace),
				},
				ContextDependent: true,
			},
			Resource: model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: pdb.Namespace, Name: pdb.Name},
		})
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// pdbDaemonsetUnverifiableForm reports whether the PDB's spec takes a form
// this project's model of the disruption controller cannot guarantee works
// for DaemonSet pods (see PDBDaemonsetScale's doc comment for the exact
// boundary), and a human-readable field name/value pair for evidence and
// the Cause message.
func pdbDaemonsetUnverifiableForm(spec policyv1.PodDisruptionBudgetSpec) (mode, value string, unverifiable bool) {
	if spec.MaxUnavailable != nil {
		return "maxUnavailable", spec.MaxUnavailable.String(), true
	}
	if spec.MinAvailable != nil && spec.MinAvailable.Type == intstr.String {
		return "minAvailable", spec.MinAvailable.String(), true
	}
	return "", "", false
}

// pdbDaemonsetSeverity classifies whether the guaranteed failure is already
// observed on the live object (High) or not yet confirmed by the
// controller (Medium): the mechanism itself is deterministic once matched,
// but this project reports what has actually happened, not only what will.
func pdbDaemonsetSeverity(pdb policyv1.PodDisruptionBudget) (model.Severity, []string) {
	if pdb.Status.ObservedGeneration < pdb.Generation {
		return model.SeverityMedium, []string{"disruptionAllowedCondition=not yet observed at the current generation"}
	}
	for _, cond := range pdb.Status.Conditions {
		if cond.Type != policyv1.DisruptionAllowedCondition {
			continue
		}
		if cond.Status == metav1.ConditionFalse && cond.Reason == policyv1.SyncFailedReason {
			return model.SeverityHigh, []string{fmt.Sprintf("disruptionAllowedCondition=%s/%s", cond.Status, cond.Reason)}
		}
		return model.SeverityMedium, []string{fmt.Sprintf("disruptionAllowedCondition=%s/%s", cond.Status, cond.Reason)}
	}
	return model.SeverityMedium, []string{"disruptionAllowedCondition=absent"}
}
