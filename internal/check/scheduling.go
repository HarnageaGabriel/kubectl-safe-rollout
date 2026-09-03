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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// SchedulingConstraintsFeasibilityCheckID is the stable identifier for this
// check.
const SchedulingConstraintsFeasibilityCheckID = "scheduling-constraints-feasibility"

// defaultSchedulerName is the literal value used by every corev1.PodSpec
// whose spec.schedulerName is left unset ("if unspecified, the pod will be
// dispatched by the default scheduler", per PodSpec's doc comment in
// k8s.io/api/core/v1/types.go). No exported constant for it exists in
// k8s.io/api, the same situation already documented for the two special
// PriorityClass keywords in priorityclass.go.
const defaultSchedulerName = "default-scheduler"

// SchedulingConstraintsFeasibility checks whether a workload's
// topologySpreadConstraints and required pod self-anti-affinity
// (podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution whose
// labelSelector matches the workload's own pod labels) can actually be
// satisfied by the cluster's current node topology. Both `check`'s existing
// checks and `watch`'s reactive diagnosers are silent here: check has
// nothing that reads cluster-wide node topology today, and watch's Pending
// diagnoser (internal/diagnose/pending.go) only reports the scheduler's
// FailedScheduling event text after the fact — this check exists so the
// same class of failure surfaces before the rollout starts.
//
// What this check evaluates, and the verified mechanism behind it:
//
//   - topologySpreadConstraints with whenUnsatisfiable=DoNotSchedule: per
//     the TopologySpreadConstraint doc comment (k8s.io/api/core/v1/types.go),
//     maxSkew bounds the difference between a topology's pod count and the
//     GLOBAL MINIMUM, and the global minimum is treated as zero whenever the
//     number of eligible domains is below minDomains (nil behaves as 1). The
//     consequence, verified live on a kind cluster: once the eligible domain
//     count reaches minDomains, spreading replicas as evenly as possible
//     always yields an achievable skew of 0 or 1, so a DoNotSchedule
//     constraint is NEVER infeasible on domain-count grounds alone past that
//     point — infeasibility only arises from the minDomains-shortfall
//     clause, in which case capacity is bounded at (eligible domains) *
//     maxSkew.
//   - topologySpreadConstraints with whenUnsatisfiable=ScheduleAnyway: a
//     Score-plugin preference only, per the same doc comment; it can never
//     make a pod unschedulable. The only finding this check emits for it is
//     a Low "dead configuration" one when zero live candidate nodes carry
//     the constraint's topologyKey at all, meaning the preference can never
//     influence scheduling.
//   - required pod self-anti-affinity: "at most one pod of this workload per
//     topology domain" is a hard capacity equal to the domain count, after
//     excluding nodes whose taints the workload does not tolerate (using
//     k8s.io/component-helpers's FindMatchingUntoleratedTaint, restricted to
//     the NoSchedule/NoExecute taint effects that actually block new pod
//     placement — PreferNoSchedule is a soft repel, documented as such on
//     corev1.TaintEffect, and is not modeled here).
//
// What this check deliberately does NOT evaluate — each omission is a
// scoping decision, not an oversight:
//
//   - node resource capacity (CPU/memory/ephemeral-storage): a distinct
//     question from topology, already partly covered by quota-headroom for
//     the ResourceQuota angle; actual node capacity is out of scope.
//   - existing pod placement or the cluster's current actual skew: this
//     check reasons about domain CAPACITY, not the live distribution of
//     already-running pods, which would require a cluster-wide Pod List this
//     check does not perform.
//   - node Ready condition: an eligible node that is currently NotReady is
//     still counted as a candidate; this check does not attempt to predict
//     whether it will recover.
//   - cluster-autoscaler: a cluster that can add nodes on demand may resolve
//     an apparent infeasibility this check cannot see; this check reasons
//     about the node topology as it exists right now.
//   - topologySpreadConstraint.matchLabelKeys and PodAffinityTerm's
//     matchLabelKeys/mismatchLabelKeys: revision-scoped pod counting: out of
//     scope. Any anti-affinity term that sets either is skipped entirely.
//   - PodAffinityTerm.namespaces / namespaceSelector: cross-namespace
//     anti-affinity is out of scope. Any term that sets either is skipped.
//   - anti-affinity against OTHER workloads' pods: evaluating that would
//     require a live cluster-wide Pod List to know what else could occupy a
//     domain; this check only evaluates a term whose labelSelector matches
//     the WORKLOAD'S OWN pod labels (self-anti-affinity), the one case whose
//     capacity is derivable from node topology alone.
//   - a candidate node that lacks the anti-affinity term's topologyKey
//     label entirely: rather than assume how the scheduler treats an
//     unlabeled node for this purpose, the whole term is skipped for that
//     workload — a deliberate refusal to conclude, not an oversight.
//   - preferredDuringSchedulingIgnoredDuringExecution (the soft form of pod
//     (anti-)affinity): never blocks scheduling, and no Low no-op finding is
//     emitted for it either, unlike the ScheduleAnyway spread case — that
//     would rest on an unverified implementation detail this design
//     deliberately avoids depending on.
//   - pod affinity (the positive form): a different question ("must
//     co-locate with"), not evaluated here.
//   - non-default schedulers: this check's whole capacity model asserts
//     kube-scheduler-specific behavior (the exact minDomains/maxSkew
//     semantics documented above); a workload whose spec.schedulerName names
//     anything other than "default-scheduler" is skipped before any API
//     call, since nothing here can be claimed about a third-party
//     scheduler's behavior.
//
// This check needs `list` on the cluster-scoped Node resource: a wider
// permission than any other check in this project requires (compare
// priorityclass-exists/serviceaccount-exists, which only `get` one named
// object). A namespace-scoped read-only ServiceAccount commonly will not
// have it — that is expected, and this check degrades to Result.Skipped like
// every other check when the List call fails, rather than treating the
// absence of permission as evidence of anything about the cluster.
type SchedulingConstraintsFeasibility struct{}

// ID implements check.Check.
func (SchedulingConstraintsFeasibility) ID() string {
	return SchedulingConstraintsFeasibilityCheckID
}

// Run implements check.Check.
func (c SchedulingConstraintsFeasibility) Run(ctx context.Context, target Target) (Result, error) {
	replicas := target.Workload.Replicas()
	if replicas == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	affinity := target.Workload.Affinity()
	spreadConstraints := target.Workload.TopologySpreadConstraints()
	antiAffinityTerms := requiredSelfAntiAffinityTerms(affinity, target.Workload.PodLabels())
	if len(spreadConstraints) == 0 && len(antiAffinityTerms) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	schedulerName := target.Workload.SchedulerName()
	if schedulerName != "" && schedulerName != defaultSchedulerName {
		// This check's whole capacity model is kube-scheduler-specific: see
		// the "non-default schedulers" bullet in the type doc comment.
		return Result{CheckID: c.ID()}, nil
	}

	nodeList, err := target.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("Node list is not accessible: %v", err)), nil
	}

	nodeSelector := target.Workload.NodeSelector()
	hasNodeConstraints := len(nodeSelector) > 0 ||
		(affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil)
	requiredAffinity := nodeaffinity.NewRequiredNodeAffinity(nodeSelector, affinity)

	var allCandidates, candidates []corev1.Node
	for _, n := range nodeList.Items {
		ok, err := requiredAffinity.Match(&n)
		if err != nil || !ok {
			continue
		}
		allCandidates = append(allCandidates, n)
		if !n.Spec.Unschedulable {
			candidates = append(candidates, n)
		}
	}
	cordonedCount := len(allCandidates) - len(candidates)

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	resource := model.ResourceRef{Kind: target.Workload.Kind(), Namespace: target.Namespace, Name: target.Workload.Name()}

	if len(allCandidates) == 0 {
		return Result{CheckID: c.ID(), Findings: []model.Finding{zeroCandidateNodesFinding(workloadRef, resource)}}, nil
	}

	surge, surgeOK := surgeCount(target.Workload)
	strategy := target.Workload.UpdateStrategy()
	var maxUnavailable int32
	maxUnavailableOK := false
	if strategy.MaxUnavailable != nil {
		v, err := intstr.GetScaledValueFromIntOrPercent(strategy.MaxUnavailable, int(replicas), false)
		if err == nil {
			maxUnavailable = int32(v)
			maxUnavailableOK = true
		}
	}
	surgeCtx := surgeContext{replicas: replicas, surge: surge, surgeOK: surgeOK, maxUnavailable: maxUnavailable, maxUnavailableOK: maxUnavailableOK}

	var findings []model.Finding
	for _, tsc := range spreadConstraints {
		if tsc.MaxSkew <= 0 {
			// Invalid per the API's own contract ("It's a required field.
			// Default value is 1 and 0 is not allowed."): nothing to
			// evaluate.
			continue
		}
		if f, ok := evaluateSpreadConstraint(tsc, allCandidates, candidates, cordonedCount, hasNodeConstraints, surgeCtx, workloadRef, resource); ok {
			findings = append(findings, f)
		}
	}
	for _, term := range antiAffinityTerms {
		if f, ok := evaluateAntiAffinityTerm(term, allCandidates, candidates, cordonedCount, target.Workload.Tolerations(), surgeCtx, workloadRef, resource); ok {
			findings = append(findings, f)
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// surgeContext bundles the replica/surge/maxUnavailable numbers every
// capacity-vs-demand comparison in this file needs, to avoid a five/six-value
// parameter list on every helper.
type surgeContext struct {
	replicas         int32
	surge            int32
	surgeOK          bool
	maxUnavailable   int32
	maxUnavailableOK bool
}

// requiredSelfAntiAffinityTerms filters
// affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
// down to the terms this check can soundly evaluate: no cross-namespace
// scope, no matchLabelKeys/mismatchLabelKeys, and a labelSelector that
// matches the workload's own pod labels (self-anti-affinity). See the type
// doc comment for why each excluded shape is out of scope.
func requiredSelfAntiAffinityTerms(affinity *corev1.Affinity, podLabels map[string]string) []corev1.PodAffinityTerm {
	if affinity == nil || affinity.PodAntiAffinity == nil {
		return nil
	}
	set := labels.Set(podLabels)
	var terms []corev1.PodAffinityTerm
	for _, term := range affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey == "" {
			continue
		}
		if len(term.Namespaces) > 0 || term.NamespaceSelector != nil {
			continue
		}
		if len(term.MatchLabelKeys) > 0 || len(term.MismatchLabelKeys) > 0 {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
		if err != nil || !selector.Matches(set) {
			continue
		}
		terms = append(terms, term)
	}
	return terms
}

// evaluateSpreadConstraint implements the DoNotSchedule/ScheduleAnyway logic
// documented on SchedulingConstraintsFeasibility. ok=false means "no finding
// for this constraint", which includes both the feasible case and the
// deliberate nodeAffinityPolicy=Ignore refusal to conclude.
func evaluateSpreadConstraint(
	tsc corev1.TopologySpreadConstraint,
	allCandidates, candidates []corev1.Node,
	cordonedCount int,
	hasNodeConstraints bool,
	sc surgeContext,
	workloadRef string,
	resource model.ResourceRef,
) (model.Finding, bool) {
	if tsc.NodeAffinityPolicy != nil && *tsc.NodeAffinityPolicy == corev1.NodeInclusionPolicyIgnore && hasNodeConstraints {
		// The capacity formula below assumes the same node set the
		// scheduler itself would use; that assumption is not sound when the
		// constraint ignores node affinity/selector but the workload has
		// one. See the type doc comment.
		return model.Finding{}, false
	}

	dLive := domainCount(candidates, tsc.TopologyKey)

	if tsc.WhenUnsatisfiable == corev1.ScheduleAnyway {
		if dLive > 0 {
			return model.Finding{}, false
		}
		return deadSpreadConstraintFinding(tsc, workloadRef, resource), true
	}

	// DoNotSchedule is both the explicit value and the documented default
	// for this required field.
	minDomains := int32(1)
	if tsc.MinDomains != nil {
		minDomains = *tsc.MinDomains
	}
	dAll := domainCount(allCandidates, tsc.TopologyKey)

	capLive, unlimitedLive := spreadCapacity(dLive, minDomains, tsc.MaxSkew)
	if !capacityInfeasible(capLive, unlimitedLive, sc) {
		return model.Finding{}, false
	}
	capAll, unlimitedAll := spreadCapacity(dAll, minDomains, tsc.MaxSkew)
	infeasibleAll := capacityInfeasible(capAll, unlimitedAll, sc)

	severity := model.SeverityHigh
	if !infeasibleAll {
		severity = model.SeverityMedium
	}
	return spreadInfeasibleFinding(tsc, workloadRef, resource, severity, dLive, dAll, minDomains, capLive, cordonedCount, sc), true
}

// evaluateAntiAffinityTerm mirrors evaluateSpreadConstraint for a single
// required self-anti-affinity term.
func evaluateAntiAffinityTerm(
	term corev1.PodAffinityTerm,
	allCandidates, candidates []corev1.Node,
	cordonedCount int,
	tolerations []corev1.Toleration,
	sc surgeContext,
	workloadRef string,
	resource model.ResourceRef,
) (model.Finding, bool) {
	if anyNodeMissingTopologyKey(allCandidates, term.TopologyKey) {
		// Deliberate refusal to conclude: see the type doc comment.
		return model.Finding{}, false
	}

	capLive := antiAffinityDomainCount(candidates, term.TopologyKey, tolerations)
	if !capacityInfeasible(int64(capLive), false, sc) {
		return model.Finding{}, false
	}
	capAll := antiAffinityDomainCount(allCandidates, term.TopologyKey, tolerations)
	infeasibleAll := capacityInfeasible(int64(capAll), false, sc)

	severity := model.SeverityHigh
	if !infeasibleAll {
		severity = model.SeverityMedium
	}
	return antiAffinityInfeasibleFinding(term, workloadRef, resource, severity, int64(capLive), cordonedCount, sc), true
}

// domainCount counts the distinct non-empty values of topologyKey among the
// given nodes. A node that does not carry the label at all does not belong
// to any domain and is not counted, matching how the scheduler's
// PodTopologySpread plugin treats it.
func domainCount(nodes []corev1.Node, topologyKey string) int32 {
	seen := map[string]struct{}{}
	for _, n := range nodes {
		v, ok := n.Labels[topologyKey]
		if !ok {
			continue
		}
		seen[v] = struct{}{}
	}
	return int32(len(seen))
}

// anyNodeMissingTopologyKey reports whether any node in the set lacks the
// given label key entirely, the trigger for this check's deliberate
// refusal to evaluate an anti-affinity term (see the type doc comment).
func anyNodeMissingTopologyKey(nodes []corev1.Node, topologyKey string) bool {
	for _, n := range nodes {
		if _, ok := n.Labels[topologyKey]; !ok {
			return true
		}
	}
	return false
}

// blockingTaintEffects are the taint effects that actually prevent a new pod
// from being placed on a node, per the documented contract of
// corev1.TaintEffect: NoSchedule and NoExecute. PreferNoSchedule is
// documented as a soft repel ("the scheduler tries not to schedule ... rather
// than prohibiting") and is deliberately excluded, the same filter
// kube-scheduler's own TaintToleration plugin applies before calling
// FindMatchingUntoleratedTaint (k8s.io/kubernetes's
// pkg/scheduler/framework/plugins/helper.DoNotScheduleTaintsFilterFunc,
// which cannot be imported here — not part of a published, importable
// module — so the two-line filter is reproduced directly instead).
func blockingTaintEffects(t *corev1.Taint) bool {
	return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
}

// nodeToleratesBlockingTaints reports whether a new pod of this workload can
// actually land on node, given its tolerations. enableComparisonOperators is
// false: Gt/Lt toleration operators are a narrow, less common case, and
// treating them as non-matching (the conservative behavior when disabled) is
// the same default FindMatchingUntoleratedTaint's own callers fall back to
// when the feature is not explicitly known to be enabled on the cluster.
func nodeToleratesBlockingTaints(node corev1.Node, tolerations []corev1.Toleration) bool {
	_, untolerated := corev1helpers.FindMatchingUntoleratedTaint(klog.Background(), node.Spec.Taints, tolerations, blockingTaintEffects, false)
	return !untolerated
}

// antiAffinityDomainCount counts the distinct topologyKey domains among
// nodes that carry the label AND that a new pod of this workload can
// actually be placed on (taint/toleration filtered). Unlike domainCount,
// taints are modeled here: see the type doc comment for why spread and
// anti-affinity treat taints differently.
func antiAffinityDomainCount(nodes []corev1.Node, topologyKey string, tolerations []corev1.Toleration) int32 {
	seen := map[string]struct{}{}
	for _, n := range nodes {
		v, ok := n.Labels[topologyKey]
		if !ok {
			continue
		}
		if !nodeToleratesBlockingTaints(n, tolerations) {
			continue
		}
		seen[v] = struct{}{}
	}
	return int32(len(seen))
}

// spreadCapacity implements the minDomains-shortfall math documented on
// SchedulingConstraintsFeasibility. unlimited=true means "never infeasible
// on domain-count grounds alone", the case once domainCount reaches
// minDomains.
func spreadCapacity(domains, minDomains, maxSkew int32) (capacity int64, unlimited bool) {
	if int64(domains) >= int64(minDomains) {
		return 0, true
	}
	return int64(domains) * int64(maxSkew), false
}

// capacityInfeasible implements decision 7: only the deterministic surge
// deadlock is flagged (capacity short of replicas alone, or short of the
// surge peak with maxUnavailable == 0), never the nondeterministic
// margin-erosion case of a surge peak with maxUnavailable > 0.
func capacityInfeasible(capacity int64, unlimited bool, sc surgeContext) bool {
	if unlimited {
		return false
	}
	if capacity < int64(sc.replicas) {
		return true
	}
	if sc.surgeOK && sc.maxUnavailableOK && sc.maxUnavailable == 0 && capacity < int64(sc.replicas)+int64(sc.surge) {
		return true
	}
	return false
}

func zeroCandidateNodesFinding(workloadRef string, resource model.ResourceRef) model.Finding {
	return model.Finding{
		CheckID:  SchedulingConstraintsFeasibilityCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%s's nodeSelector/nodeAffinity matches zero nodes in the cluster: no pod of this workload can be scheduled at all",
			workloadRef,
		),
		Evidence: []string{"matchingNodes=0"},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("check %s's nodeSelector/affinity against the labels actually present on cluster nodes, or add nodes carrying the required labels; the correct fix depends on where this workload is meant to run", workloadRef),
			Commands:         []string{"kubectl get nodes --show-labels"},
			ContextDependent: true,
		},
		Resource: resource,
	}
}

func deadSpreadConstraintFinding(tsc corev1.TopologySpreadConstraint, workloadRef string, resource model.ResourceRef) model.Finding {
	return model.Finding{
		CheckID:  SchedulingConstraintsFeasibilityCheckID,
		Severity: model.SeverityLow,
		Cause: fmt.Sprintf(
			"%s has a topologySpreadConstraint on %q with whenUnsatisfiable=ScheduleAnyway, but no live candidate node carries that label: the constraint can never influence scheduling and is dead configuration",
			workloadRef, tsc.TopologyKey,
		),
		Evidence: []string{fmt.Sprintf("topologyKey=%s liveCandidateNodesWithLabel=0", tsc.TopologyKey)},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("remove the topologySpreadConstraint on %q if it is no longer meaningful, or label nodes with %q if spreading across it is still intended", tsc.TopologyKey, tsc.TopologyKey),
			Commands:         []string{fmt.Sprintf("kubectl get nodes -L %s", tsc.TopologyKey)},
			ContextDependent: true,
		},
		Resource: resource,
	}
}

func spreadInfeasibleFinding(
	tsc corev1.TopologySpreadConstraint,
	workloadRef string,
	resource model.ResourceRef,
	severity model.Severity,
	dLive, dAll, minDomains int32,
	capLive int64,
	cordonedCount int,
	sc surgeContext,
) model.Finding {
	cause := fmt.Sprintf(
		"%s's topologySpreadConstraint on %q (maxSkew=%d, minDomains=%d, whenUnsatisfiable=DoNotSchedule) has only %d eligible domain(s) below minDomains, capping capacity at %d pod(s), but %d replicas are requested",
		workloadRef, tsc.TopologyKey, tsc.MaxSkew, minDomains, dLive, capLive, sc.replicas,
	)
	evidence := []string{
		fmt.Sprintf("topologyKey=%s maxSkew=%d minDomains=%d", tsc.TopologyKey, tsc.MaxSkew, minDomains),
		fmt.Sprintf("eligibleDomains=%d capacity=%d replicas=%d", dLive, capLive, sc.replicas),
	}
	if severity == model.SeverityMedium {
		cause += fmt.Sprintf("; %d cordoned node(s) are excluded from this count and, if uncordoned, would raise the eligible domain count to %d, making the constraint feasible", cordonedCount, dAll)
		evidence = append(evidence, fmt.Sprintf("cordonedNodes=%d eligibleDomainsIncludingCordoned=%d", cordonedCount, dAll))
	}
	return model.Finding{
		CheckID:  SchedulingConstraintsFeasibilityCheckID,
		Severity: severity,
		Cause:    cause,
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("add nodes/labels to raise the eligible domain count for %q to at least minDomains, lower minDomains or raise maxSkew on %s's topologySpreadConstraint, or reduce replicas; the correct choice depends on where this workload must actually run", tsc.TopologyKey, workloadRef),
			Commands:         []string{fmt.Sprintf("kubectl get nodes -L %s", tsc.TopologyKey)},
			ContextDependent: true,
		},
		Resource: resource,
	}
}

func antiAffinityInfeasibleFinding(
	term corev1.PodAffinityTerm,
	workloadRef string,
	resource model.ResourceRef,
	severity model.Severity,
	capLive int64,
	cordonedCount int,
	sc surgeContext,
) model.Finding {
	cause := fmt.Sprintf(
		"%s requires required pod self-anti-affinity on topologyKey %q (at most one pod per domain), but only %d eligible domain(s) can actually host a pod, while %d replicas are requested",
		workloadRef, term.TopologyKey, capLive, sc.replicas,
	)
	evidence := []string{
		fmt.Sprintf("topologyKey=%s", term.TopologyKey),
		fmt.Sprintf("eligibleDomains=%d replicas=%d", capLive, sc.replicas),
	}
	if severity == model.SeverityMedium {
		cause += fmt.Sprintf("; %d cordoned node(s) are excluded from this count and, if uncordoned, would make the constraint feasible", cordonedCount)
		evidence = append(evidence, fmt.Sprintf("cordonedNodes=%d", cordonedCount))
	}
	return model.Finding{
		CheckID:  SchedulingConstraintsFeasibilityCheckID,
		Severity: severity,
		Cause:    cause,
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("add nodes/labels to raise the eligible domain count for %q, loosen the self-anti-affinity requirement on %s, or reduce replicas; the correct choice depends on why the anti-affinity rule exists", term.TopologyKey, workloadRef),
			Commands:         []string{fmt.Sprintf("kubectl get nodes -L %s", term.TopologyKey)},
			ContextDependent: true,
		},
		Resource: resource,
	}
}
