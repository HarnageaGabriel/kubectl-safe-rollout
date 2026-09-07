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
	"strings"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// PDBEvictionBlockedCheckID is the stable identifier for this check.
const PDBEvictionBlockedCheckID = "pdb-eviction-blocked"

// PDBEvictionBlocked checks two facts about the Eviction API distinct from
// what pdb-consistency and pdb-daemonset-scale already cover.
//
// Deliberate technical note, same one already spelled out at the top of
// pdb.go: a PodDisruptionBudget is enforced by the Eviction API (used by
// `kubectl drain`, the cluster-autoscaler, and the descheduler), never by
// the Deployment/StatefulSet/DaemonSet controller when it replaces pods
// during a rolling update (that path issues a direct pod DELETE). Every
// finding here is therefore about a concurrent drain/autoscaler/descheduler
// action being blocked, never about the rollout itself.
//
// Fact A — more than one matching PDB is a configuration the eviction
// subresource refuses outright, not just a stricter budget. Verified in
// k8s.io/kubernetes@v1.36.1 pkg/registry/core/pod/storage/eviction.go,
// EvictionREST.Create (func at line 129): it calls getPodDisruptionBudgets
// (line 487) to collect every PDB in the pod's namespace whose selector
// matches the pod's labels, then at line 223:
//
//	if len(pdbs) > 1 {
//		rtStatus = &metav1.Status{
//			Status:  metav1.StatusFailure,
//			Message: "This pod has more than one PodDisruptionBudget, which the eviction subresource does not support.",
//			Code:    500,
//		}
//		return nil
//	}
//
// There is no feature gate and no fallback: every eviction of a pod matched
// by two or more PDBs fails with that exact message, forever, regardless of
// how generous either budget is. This is invisible from either PDB's status
// alone, because the disruption controller (see Fact B below) evaluates
// each PDB independently and can report disruptionsAllowed>0 on both at the
// same time.
//
// getPodDisruptionBudgets itself (eviction.go lines 487-511) is the same
// selector-matching contract this check's own matching loop below mirrors:
// metav1.LabelSelectorAsSelector then selector.Matches(pod labels), with NO
// selector.Empty() guard (lines 498-505). A nil Spec.Selector already
// resolves to labels.Nothing() and is filtered out by Matches on its own;
// an explicit empty ({}) Selector resolves to labels.Everything() and the
// real eviction handler treats it as matching every pod in the namespace —
// adding an Empty() guard here would silently exempt exactly the PDB shape
// this check exists to catch. Same reasoning already applied to
// pdb-consistency and pdb-daemonset-scale.
//
// Fact B — a live disruptionsAllowed of 0 can mean something the Spec
// arithmetic alone cannot see. Verified in
// k8s.io/kubernetes@v1.36.1 pkg/controller/disruption/disruption.go,
// updatePdbStatus (line 996): disruptionsAllowed = currentHealthy -
// desiredHealthy, floored at 0 (also floored at 0 whenever expectedCount<=0,
// lines 1003-1006). currentHealthy is countHealthyPods' live count of Ready
// pods under the PDB (line 919), not this workload's spec.replicas.
// desiredHealthy, computed by getExpectedPodCount (lines 813-853), is
// derived from expectedCount for maxUnavailable (any form) and for a
// percentage minAvailable; and getExpectedScale's own comment (lines
// 855-860) states the denominator explicitly:
//
//	SUM_{all c in C} scale(c)
//	where C is the union of C_p1, C_p2, ..., C_pN
//
// i.e. the combined declared scale of every controller that owns a pod
// under the PDB's selector, not necessarily only this workload. So a live
// disruptionsAllowed of 0 in steady state can mean: a pod is not Ready, a
// PDB shared by more than one workload whose combined scale does not match
// this workload's replica count, or a sync that has not caught up yet — the
// Spec-only arithmetic in pdb-consistency's allowedDisruptions cannot see
// any of that, only what disruptionsAllowed *should* be if the live world
// matched the Spec exactly.
//
// This is reported at Medium, and only when every one of these is true
// (checkAndDecrement, eviction.go lines 424-436, mirrors the same
// ObservedGeneration/DisruptionsAllowed preconditions before actually
// rejecting an eviction):
//   - exactly one PDB matches (2+ is Fact A, not repeated here);
//   - pdb.Status.ObservedGeneration == pdb.Generation (a status the
//     controller has not caught up on yet, ObservedGeneration < Generation,
//     is a transient state, not evidence of anything — same precondition
//     checkAndDecrement itself requires before trusting the status at all);
//   - pdb.Status.DisruptionsAllowed == 0;
//   - pdb.Status.DisruptedPods is empty (a pod evicted moments ago is
//     subtracted from currentHealthy for up to DeletionTimeout, 2 minutes —
//     disruption.go line 70 — a disruptionsAllowed of 0 during that window
//     is expected and clears on its own, not a finding);
//   - target.Workload.RolloutComplete() is true (during a rollout, pods
//     that are not yet Ready are normal and expected: disruptionsAllowed=0
//     is the correct state, not a problem this check should surface);
//   - the Spec-only arithmetic (pdb-consistency's allowedDisruptions,
//     reused here directly, same package) says headroom should be > 0. If
//     the Spec arithmetic already says 0, pdb-consistency already reports
//     that PDB as a High finding: repeating it here at Medium would be
//     noise, not new information.
//
// Known limitation, declared explicitly rather than hidden: this check
// never lists Pods to find ones that match a PDB's selector but are not
// owned by this workload's controller (an "unmanaged pod" in
// getExpectedScale's own terminology) — that would need a namespace-wide
// Pod list plus owner-reference resolution, out of scope for this version.
//
// Same approximation pdb-consistency and pdb-daemonset-scale already
// accept, restated here: matching uses Workload.PodLabels() (the pod
// template), while the real eviction handler matches the LIVE labels of
// the specific pod being evicted — a pod from an old revision mid-rollout
// carries a different pod-template-hash and this check cannot see it.
type PDBEvictionBlocked struct{}

// ID implements check.Check.
func (PDBEvictionBlocked) ID() string { return PDBEvictionBlockedCheckID }

// Run implements check.Check.
func (c PDBEvictionBlocked) Run(ctx context.Context, target Target) (Result, error) {
	replicas := target.Workload.Replicas()
	if replicas == 0 {
		// No running pod for the Eviction API to ever act on: same guard,
		// and the same reasoning, as pdb-consistency.
		return Result{CheckID: c.ID()}, nil
	}

	// The PDB list must happen before any other early return below, so that
	// a restricted RBAC scenario always exercises this check's real degrade
	// path: see the identical note on ordering in pdb.go/pdbdaemonset.go.
	pdbList, err := target.Client.PolicyV1().PodDisruptionBudgets(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("PodDisruptionBudget list is not accessible: %v", err)), nil
	}

	podLabels := labels.Set(target.Workload.PodLabels())
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	var matched []policyv1.PodDisruptionBudget
	for _, pdb := range pdbList.Items {
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		// No selector.Empty() guard: see this check's doc comment above for
		// the full reasoning, identical to pdb-consistency and
		// pdb-daemonset-scale.
		if err != nil || !selector.Matches(podLabels) {
			continue
		}
		matched = append(matched, pdb)
	}

	var findings []model.Finding
	switch {
	case len(matched) > 1:
		findings = append(findings, factAFinding(target, workloadRef, matched))
	case len(matched) == 1:
		if f, ok := factBFinding(target, workloadRef, replicas, matched[0]); ok {
			findings = append(findings, f)
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// factAFinding builds the High finding for more than one PodDisruptionBudget
// matching the same pods: see this file's doc comment for the verified
// mechanism and the exact eviction subresource error message.
func factAFinding(target Target, workloadRef string, matched []policyv1.PodDisruptionBudget) model.Finding {
	names := make([]string, 0, len(matched))
	for _, pdb := range matched {
		names = append(names, pdb.Name)
	}
	namesJoined := strings.Join(names, ", ")

	return model.Finding{
		CheckID:  PDBEvictionBlockedCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%d PodDisruptionBudgets (%s) match %s's pods: the eviction subresource does not support more than one matching PodDisruptionBudget per pod and rejects every eviction attempt with \"This pod has more than one PodDisruptionBudget, which the eviction subresource does not support.\" — a node drain, cluster-autoscaler scale-down, or descheduler action against any of these pods will fail permanently, independent of how generous either budget is",
			len(matched), namesJoined, workloadRef,
		),
		Evidence: []string{
			fmt.Sprintf("matchingPodDisruptionBudgets=%s", namesJoined),
		},
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"make the selectors of %s disjoint, or delete one of them, so that at most one PodDisruptionBudget matches %s's pods; widening either budget does not fix this, the eviction subresource rejects the request regardless of the allowed disruption count",
				namesJoined, workloadRef,
			),
			Commands:         []string{fmt.Sprintf("kubectl get pdb %s -n %s", strings.Join(names, " "), target.Namespace)},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{Kind: target.Workload.Kind(), Namespace: target.Namespace, Name: target.Workload.Name()},
	}
}

// factBFinding builds the Medium finding for a single matching PodDisruptionBudget
// whose live status already reports zero disruption headroom for reasons the
// Spec-only arithmetic cannot see. See this file's doc comment for every
// precondition and why each one is required.
func factBFinding(target Target, workloadRef string, replicas int32, pdb policyv1.PodDisruptionBudget) (model.Finding, bool) {
	if pdb.Status.ObservedGeneration != pdb.Generation {
		return model.Finding{}, false
	}
	if pdb.Status.DisruptionsAllowed != 0 {
		return model.Finding{}, false
	}
	if len(pdb.Status.DisruptedPods) != 0 {
		return model.Finding{}, false
	}
	if !target.Workload.RolloutComplete() {
		return model.Finding{}, false
	}

	allowed, mode, err := allowedDisruptions(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable, replicas)
	if err != nil || allowed <= 0 {
		// Either an invalid PDB spec (out of scope, same as pdb-consistency)
		// or the Spec arithmetic already predicts zero headroom: that case
		// is already reported by pdb-consistency at High, repeating it here
		// would be noise.
		return model.Finding{}, false
	}

	return model.Finding{
		CheckID:  PDBEvictionBlockedCheckID,
		Severity: model.SeverityMedium,
		Cause: fmt.Sprintf(
			"PodDisruptionBudget %q selecting %s's pods reports disruptionsAllowed=0 live, even though its %s=%s over %d replicas should leave headroom: currentHealthy=%d, desiredHealthy=%d, expectedPods=%d — a node drain, cluster-autoscaler scale-down, or descheduler action against these pods is blocked right now for a reason the budget's spec alone does not show (an unready pod, or a PDB whose selector also covers pods from another controller)",
			pdb.Name, workloadRef, mode, pdbSpecValue(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable), replicas,
			pdb.Status.CurrentHealthy, pdb.Status.DesiredHealthy, pdb.Status.ExpectedPods,
		),
		Evidence: []string{
			fmt.Sprintf("%s=%s", mode, pdbSpecValue(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable)),
			fmt.Sprintf("specAllowedDisruptions=%d", allowed),
			fmt.Sprintf("status.disruptionsAllowed=%d", pdb.Status.DisruptionsAllowed),
			fmt.Sprintf("status.currentHealthy=%d", pdb.Status.CurrentHealthy),
			fmt.Sprintf("status.desiredHealthy=%d", pdb.Status.DesiredHealthy),
			fmt.Sprintf("status.expectedPods=%d", pdb.Status.ExpectedPods),
		},
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"inspect PodDisruptionBudget %q's live status: compare currentHealthy/desiredHealthy/expectedPods against %s's actual pods to find the unready pod or the other controller sharing this PDB's selector; the Spec alone predicts headroom, so the cause is live cluster state, not the budget's configuration",
				pdb.Name, workloadRef,
			),
			Commands:         []string{fmt.Sprintf("kubectl get pdb %s -n %s -o jsonpath='{.status}'", pdb.Name, target.Namespace)},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: pdb.Namespace, Name: pdb.Name},
	}, true
}
