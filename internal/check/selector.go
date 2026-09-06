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
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// SelectorOverlapCheckID is the stable identifier for this check.
const SelectorOverlapCheckID = "selector-overlap"

// SelectorOverlap flags a Deployment or StatefulSet whose pod selector
// overlaps another Deployment's or StatefulSet's pod template labels in the
// same namespace.
//
// This is NOT "two live controllers steal each other's pods": that scenario
// is verified false in vanilla Kubernetes. ControllerRef plus the
// API server's owner-reference validation block it — a claim only ever
// succeeds through BaseControllerRefManager.ClaimObject, which ignores any
// object already carrying a controller OwnerReference with a different UID.
//
// The real, narrower, verified risk is an ORPHANED ReplicaSet or
// ControllerRevision — produced by `kubectl delete deployment
// --cascade=orphan`, a Helm/Argo resource-adoption mishap, or garbage
// collector lag — whose labels match another workload's selector. That
// orphan gets silently adopted by the other workload's controller: for a
// Deployment, the adopted ReplicaSet is then scaled to zero because its
// pod-template-hash does not match (a Deployment lists every ReplicaSet in
// the namespace via getReplicaSetsForDeployment and claims under its own
// selector); for a StatefulSet, an orphaned ControllerRevision matching the
// selector is adopted into revision history with no name guard the way the
// pod adoption path has, corrupting currentRevision/updateRevision/
// collisionCount and `kubectl rollout history`. Every apps workload
// controller (ReplicaSet, Deployment, StatefulSet, DaemonSet, Job) logs an
// identical "user error! more than one X is selecting pods with labels: ..."
// under the shared comment "ControllerRef will ensure we don't do anything
// crazy, but more than one item in this list nevertheless constitutes user
// error." That comment is the citation for both halves of this check's
// justification: the damage is bounded (no live pod theft), but the
// condition is genuinely flagged upstream as user error, not a benign
// coincidence.
//
// Severity is fixed Medium, never High and never varied by the other
// workload's replica count: there is no live blockage today, matching the
// "concrete but nondeterministic risk" bar already used for
// hpa-quota-headroom and requests-vs-usage. spec.selector is immutable on
// both Deployment and StatefulSet, so an overlap is permanent and one
// `kubectl scale`/orphaning event away from live regardless of how many
// replicas the other workload currently runs — that replica count is
// recorded in Evidence instead.
//
// Scope, v1 limitations stated explicitly rather than left silent:
//   - only Deployment and StatefulSet are compared, matching the two kinds
//     internal/workload supports today. DaemonSet, Job, and CronJob are
//     mechanically exposed to the same real controller mechanism but are
//     out of scope for this version, the same kind of stated limitation as
//     pvc-exists not seeing spec.volumeClaimTemplates.
//   - PodDisruptionBudget, Service, and NetworkPolicy selectors are
//     deliberately not considered: none of them drive ownership adoption,
//     only a workload controller's spec.selector, consumed by a
//     ControllerRefManager, does.
//   - cross-namespace collisions are out of scope by API contract, not by
//     limitation: ClaimObject refuses cross-namespace adoption, so listing
//     only the target's namespace is the correct boundary.
//   - this check reports the latent hazard computed from live
//     selectors/labels; it does not hunt for evidence that an orphaning
//     event has actually occurred.
//
// No watch-side counterpart exists, and none should be added: internal/
// diagnose has nothing reactive about orphan adoption today, and a reactive
// diagnoser for it would be unreliable by construction — once adoption has
// happened, the damage looks identical to a normal old-revision scale-down
// from watch's perspective, with no way to tell an adopted orphan from a
// legitimate one after the fact.
type SelectorOverlap struct{}

// ID implements check.Check.
func (SelectorOverlap) ID() string { return SelectorOverlapCheckID }

// Run implements check.Check.
func (c SelectorOverlap) Run(ctx context.Context, target Target) (Result, error) {
	selfSel, err := target.Workload.PodSelector()
	if err != nil {
		// Unreachable through admission: a workload with an invalid
		// selector could never have been created in the first place.
		return Result{CheckID: c.ID()}, nil
	}
	selfLabels := target.Workload.PodLabels()

	deployList, err := target.Client.AppsV1().Deployments(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("deployment list is not accessible: %v", err)), nil
	}
	stsList, err := target.Client.AppsV1().StatefulSets(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("statefulset list is not accessible: %v", err)), nil
	}

	candidates := make([]workload.Workload, 0, len(deployList.Items)+len(stsList.Items))
	for i := range deployList.Items {
		candidates = append(candidates, workload.FromDeployment(&deployList.Items[i]))
	}
	for i := range stsList.Items {
		candidates = append(candidates, workload.FromStatefulSet(&stsList.Items[i]))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Kind() != candidates[j].Kind() {
			return candidates[i].Kind() < candidates[j].Kind()
		}
		return candidates[i].Name() < candidates[j].Name()
	})

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	resourceRef := model.ResourceRef{Kind: target.Workload.Kind(), Namespace: target.Namespace, Name: target.Workload.Name()}

	var findings []model.Finding
	for _, other := range candidates {
		if other.UID() == target.Workload.UID() {
			// Self-comparison: the target workload always appears in its
			// own List result. UID, not name+kind, is the only identity
			// check reliable across both candidate kinds.
			continue
		}
		otherSel, err := other.PodSelector()
		if err != nil {
			// Defensive: one malformed candidate must not abort evaluation
			// of the rest.
			continue
		}

		selfMatchesOther := selfSel.Matches(labels.Set(other.PodLabels()))
		otherMatchesSelf := otherSel.Matches(labels.Set(selfLabels))
		if !selfMatchesOther && !otherMatchesSelf {
			continue
		}

		otherRef := fmt.Sprintf("%s/%s", other.Kind(), other.Name())
		evidence := []string{
			fmt.Sprintf("otherKind=%s", other.Kind()),
			fmt.Sprintf("otherName=%s", other.Name()),
			fmt.Sprintf("otherReplicas=%d", other.Replicas()),
		}
		if selfMatchesOther {
			evidence = append(evidence, "selfSelectorMatchesOtherLabels")
		}
		if otherMatchesSelf {
			evidence = append(evidence, "otherSelectorMatchesSelfLabels")
		}

		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: model.SeverityMedium,
			Cause: fmt.Sprintf(
				"%s's selector overlaps %s's pod labels (or vice versa): a live rollout controller never adopts another controller's live pods (ControllerRef blocks that), but an orphaned ReplicaSet or ControllerRevision matching this selector would be silently adopted and scaled to zero, or folded into revision history — Kubernetes itself treats this as user error (every apps workload controller logs an identical \"user error!\" guard line for exactly this condition)",
				workloadRef, otherRef,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"spec.selector is immutable on both Deployment and StatefulSet: fixing this means recreating one of %s or %s with a distinguishing label, which deletes that workload's pods. Which of the two is misconfigured is a judgment call this tool cannot make.",
					workloadRef, otherRef,
				),
				Commands: []string{
					fmt.Sprintf("kubectl get deploy,sts -n %s -o custom-columns=KIND:.kind,NAME:.metadata.name,SELECTOR:.spec.selector.matchLabels", target.Namespace),
				},
				ContextDependent: true,
			},
			Resource: resourceRef,
		})
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}
