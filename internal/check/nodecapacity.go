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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	podresource "k8s.io/component-helpers/resource"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// NodeCapacityFeasibilityCheckID is the stable identifier for this check.
const NodeCapacityFeasibilityCheckID = "node-capacity-feasibility"

// NodeCapacityFeasibility checks whether a single pod of this workload's
// shape could ever be admitted onto any node the scheduler would consider
// for it, on the cpu/memory dimensions alone. This is a distinct question
// from scheduling-constraints-feasibility (topology: spread/anti-affinity/
// node selection) and from quota-headroom (namespace ResourceQuota): a
// workload can satisfy both of those and still request more cpu/memory
// than any node in the cluster can ever allocate to a single pod, in which
// case the pod stays Pending forever regardless of how many replicas are
// requested or how much quota headroom exists.
//
// Mechanism, verified against the vendored source of
// k8s.io/kubernetes@v1.36.1/pkg/scheduler/framework/plugins/noderesources/
// fit.go, function fitsRequest: the scheduler's NodeResourcesFit plugin
// compares a pod's per-resource request against
// nodeInfo.GetAllocatable() (never nodeInfo.GetCapacity() — Allocatable is
// the resource actually available for scheduling, Capacity minus any
// kube-reserved/system-reserved/eviction-threshold carve-out, per the doc
// comment on NodeStatus.Allocatable in
// k8s.io/api@v0.37.0/core/v1/types.go:7079-7086). When
// podRequest > nodeInfo.GetAllocatable() for some resource (the BARE
// allocatable, not allocatable-minus-currently-requested), fitsRequest
// marks that node's InsufficientResource as Unresolvable: true — see the
// field's own doc comment ("whether this node could be schedulable for the
// pod by the preemption", fit.go:~668-670) — no preemption can ever free
// enough space on that node, because the shortfall exists even before any
// other pod's requests are subtracted. This check emits exactly that
// permanent fact — podRequest > node.Status.Allocatable[key] for EVERY
// node this workload's pod could ever land on — and nothing else.
//
// Deliberately out of scope, decided before writing this check, not
// discovered as a limitation afterward:
//
//   - The residual/transitory fact (Allocatable minus the node's
//     CURRENTLY requested pods, the ordinary non-Unresolvable fit
//     failure): computing it would require a cluster-wide Pod List, a
//     permission almost never granted to a namespace-scoped read-only
//     ServiceAccount, and the number would be stale or flaky during a
//     perfectly healthy rolling update where terminating old pods free
//     exactly the space new pods need. `watch` already covers this
//     reactively via the scheduler's own FailedScheduling event. If ever
//     built, it belongs in a separate CheckID, not an extension of this
//     one.
//   - ephemeral-storage: unlike cpu/memory, a node's allocatable
//     ephemeral-storage is a function of current disk occupancy, not a
//     fixed structural fact about the node — evaluating it here would mix
//     a permanent capacity claim with a value that drifts on its own.
//   - Extended resources (nvidia.com/gpu and similar) and hugepages: a
//     device plugin that has not yet registered on a freshly (re)started
//     node reports its resource as absent or zero, transiently, on every
//     node reboot — flagging that as "no node can ever fit this pod"
//     would be a routine false positive, not a structural fact.
//   - The per-node "pods" count limit: a distinct admission dimension the
//     project does not evaluate for node capacity today.
//   - Replicas/maxSurge/maxUnavailable: the fact this check emits is about
//     a SINGLE pod of this shape never fitting anywhere, independent of
//     how many are requested — unlike quota-headroom and
//     scheduling-constraints-feasibility, no surge arithmetic applies
//     here.
//   - spec.Overhead (RuntimeClass pod overhead): the vendored
//     podresource.PodRequests function this check calls does add it when
//     present on the pod object, but the pod template read from the
//     workload manifest never carries it — RuntimeClass admission injects
//     Overhead onto the live Pod object, a step this check does not
//     simulate. The consequence is a possible understatement of the true
//     request for a RuntimeClass workload, the safe direction (fewer
//     false positives, not more).
//
// The synthetic pod this check evaluates simulates only the stage-1
// limits-to-requests copy (SetDefaults_Pod,
// k8s.io/kubernetes@v1.36.1/pkg/apis/core/v1/defaults.go:164-192 — for
// every key present in a container's declared limits but absent from its
// declared requests, the real Pod object gets requests[key] = limits[key],
// for both regular and init containers, before any scheduling decision is
// made). Verified live on kind (v1.37.0), not only from source: a
// container declaring only limits.memory: 16Gi, no requests, produced a
// real Pod with requests.memory: 16Gi copied in — confirmed Pending with
// event "Insufficient memory" against a 6-node-equivalent single-node
// cluster whose allocatable memory was below that value. A parallel
// cpu-only scenario (request cpu: "10" against 6 allocatable cpu)
// reproduced the verbatim scheduler event:
//
//	0/1 nodes are available: 1 Insufficient cpu. preemption: 0/1 nodes
//	are available: 1 Preemption is not helpful for scheduling.
//
// Init containers are included deliberately: a request declared only on
// an init container (with a small regular container) was verified live to
// leave the pod Pending exactly like an oversized regular container would
// — the aggregation is delegated entirely to
// k8s.io/component-helpers@v0.37.0/resource.PodRequests, the same
// sidecar-KEP formula already relied on for
// scheduling-constraints-feasibility's sibling concerns: regular
// containers summed, restartable ("sidecar") init containers folded into
// a running cumulative total, each non-restartable init container's
// requirement compared against that cumulative total, and the pod's
// requirement for each resource is the max of the regular-container sum
// and the largest such cumulative init-container value. This check calls
// that function rather than reimplementing the formula.
//
// The candidate node set reuses candidateNodes(), the same label-match +
// cordon-exclusion logic scheduling-constraints-feasibility.go already
// relies on, so a nodeSelector/nodeAffinity that confines this workload to
// a small node pool is respected instead of comparing against every node
// in the cluster (which would understate the real risk if larger nodes
// exist outside that pool). For Deployment/StatefulSet the schedulable
// subset (candidates: label match AND not cordoned) is used, matching
// what the scheduler would actually consider today. For DaemonSet the
// WIDER superset (allCandidates: label match only, cordoned nodes
// included) is used instead, deliberately: the DaemonSet controller
// rewrites per-pod node affinity and injects additional tolerations
// (including for node.kubernetes.io/unschedulable) before scheduling —
// the same reasons scheduling-constraints-feasibility excludes DaemonSet
// entirely — so this check cannot know which of those nodes the
// controller would actually target. Evaluating against the superset is
// the safe direction for the claim this check makes ("does not fit
// anywhere"): if the request does not fit even considering every
// label-matching node including cordoned ones, it certainly does not fit
// in the real, smaller set the controller would use. The mirror-image
// partial case for DaemonSet (fits on some eligible nodes but not others)
// is out of scope, the same boundary already accepted for
// scheduling-constraints-feasibility.
//
// Severity is always High: an Unresolvable shortfall is a permanent,
// deterministic fact, no different in kind from the guaranteed rejections
// already reported at High by serviceaccount-exists, priorityclass-exists,
// limitrange-feasibility, and pod-security-admission. cpu and memory are
// independently actionable, so a workload that violates both produces two
// separate findings, the same convention quota-headroom already uses for
// its own cpu/memory checks.
type NodeCapacityFeasibility struct{}

// ID implements check.Check.
func (NodeCapacityFeasibility) ID() string { return NodeCapacityFeasibilityCheckID }

// capacityResourceKeys is the fixed, deliberately narrow set of resources
// this check evaluates. See the type doc comment for why
// ephemeral-storage and extended resources are excluded.
var capacityResourceKeys = []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory}

// Run implements check.Check.
func (c NodeCapacityFeasibility) Run(ctx context.Context, target Target) (Result, error) {
	pod := syntheticCapacityPod(target.Workload)
	podReq := podresource.PodRequests(pod, podresource.PodResourcesOptions{})

	type wantedRequest struct {
		key corev1.ResourceName
		req resource.Quantity
	}
	var wanted []wantedRequest
	for _, key := range capacityResourceKeys {
		if v, ok := podReq[key]; ok && !v.IsZero() {
			wanted = append(wanted, wantedRequest{key: key, req: v})
		}
	}
	if len(wanted) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	schedulerName := target.Workload.SchedulerName()
	if schedulerName != "" && schedulerName != defaultSchedulerName {
		// This check's fit model is kube-scheduler-specific (fitsRequest):
		// nothing here can be claimed about a third-party scheduler, the
		// same reasoning scheduling-constraints-feasibility already
		// applies.
		return Result{CheckID: c.ID()}, nil
	}

	nodeList, err := target.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("Node list is not accessible: %v", err)), nil
	}
	if len(nodeList.Items) == 0 {
		return Skip(c.ID(), "the cluster reports zero nodes; node capacity feasibility cannot be evaluated"), nil
	}

	allCandidates, candidates, _ := candidateNodes(nodeList.Items, target.Workload.NodeSelector(), target.Workload.Affinity())
	nodeSet := candidates
	if target.Workload.Kind() == "DaemonSet" {
		// Superset direction: see the type doc comment for why cordoning
		// does not exclude a node from a DaemonSet's real candidate set.
		nodeSet = allCandidates
	}
	if len(nodeSet) == 0 {
		// Zero candidate nodes is scheduling-constraints-feasibility's fact
		// to report, not this check's: reporting it again here would be
		// redundant noise, not a distinct finding.
		return Result{CheckID: c.ID()}, nil
	}

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	resourceRef := model.ResourceRef{Kind: target.Workload.Kind(), Namespace: target.Namespace, Name: target.Workload.Name()}

	var findings []model.Finding
	var unresolvedKeys []string
	evaluated := 0
	for _, w := range wanted {
		maxAllocatable, maxNodeName, ok := largestAllocatable(nodeSet, w.key)
		if !ok {
			// No candidate node reports this key in Status.Allocatable at
			// all (e.g. every candidate just registered or is NotReady
			// with a partial status): refuse to conclude for this key
			// rather than treating the absence as zero, which would make
			// every healthy node look infinitely small.
			unresolvedKeys = append(unresolvedKeys, string(w.key))
			continue
		}
		evaluated++
		if w.req.Cmp(maxAllocatable) > 0 {
			findings = append(findings, nodeCapacityFinding(workloadRef, resourceRef, w.key, w.req, maxAllocatable, maxNodeName, len(nodeSet)))
		}
	}

	if evaluated == 0 {
		return Skip(c.ID(), fmt.Sprintf("no candidate node reports allocatable %s; node capacity feasibility cannot be evaluated", strings.Join(unresolvedKeys, ", "))), nil
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// syntheticCapacityPod builds a corev1.Pod from the workload's pod template
// for the sole purpose of computing its aggregate requests, simulating the
// stage-1 limits-to-requests copy every real Pod object receives before
// any scheduling decision (see the type doc comment). This is a narrower
// simulation than limitrange-feasibility's: no LimitRange
// default/defaultRequest backfill applies here, only the unconditional
// per-container copy.
func syntheticCapacityPod(w workload.Workload) *corev1.Pod {
	template := w.PodTemplate()
	spec := template.Spec.DeepCopy()
	applyStage1RequestsFromLimits(spec.Containers)
	applyStage1RequestsFromLimits(spec.InitContainers)
	return &corev1.Pod{Spec: *spec}
}

// applyStage1RequestsFromLimits mutates containers in place, copying each
// declared limit whose key has no matching declared request into that
// container's requests, mirroring SetDefaults_Pod's unconditional
// per-container copy (see the type doc comment for the exact source
// reference).
func applyStage1RequestsFromLimits(containers []corev1.Container) {
	for i := range containers {
		c := &containers[i]
		if len(c.Resources.Limits) == 0 {
			continue
		}
		if c.Resources.Requests == nil {
			c.Resources.Requests = corev1.ResourceList{}
		}
		for name, limit := range c.Resources.Limits {
			if _, ok := c.Resources.Requests[name]; !ok {
				c.Resources.Requests[name] = limit
			}
		}
	}
}

// largestAllocatable returns the largest Status.Allocatable[key] value
// among nodes, and the name of the node that reports it. ok is false when
// no node in the set reports this key at all — the caller must not treat
// that as zero.
func largestAllocatable(nodes []corev1.Node, key corev1.ResourceName) (best resource.Quantity, nodeName string, ok bool) {
	for _, n := range nodes {
		v, exists := n.Status.Allocatable[key]
		if !exists {
			continue
		}
		if !ok || v.Cmp(best) > 0 {
			best = v
			nodeName = n.Name
			ok = true
		}
	}
	return best, nodeName, ok
}

func nodeCapacityFinding(
	workloadRef string,
	resourceRef model.ResourceRef,
	key corev1.ResourceName,
	requested, maxAllocatable resource.Quantity,
	maxNodeName string,
	candidateCount int,
) model.Finding {
	return model.Finding{
		CheckID:  NodeCapacityFeasibilityCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%s's pod template requests %s of %s, but the largest allocatable %s among %d candidate node(s) is %s on node %q: no candidate node can ever admit a pod of this shape, and no preemption can help (kube-scheduler marks this Unresolvable)",
			workloadRef, requested.String(), key, key, candidateCount, maxAllocatable.String(), maxNodeName,
		),
		Evidence: []string{
			fmt.Sprintf("requested.%s=%s", key, requested.String()),
			fmt.Sprintf("largestAllocatable.%s=%s node=%s", key, maxAllocatable.String(), maxNodeName),
			fmt.Sprintf("candidateNodesEvaluated=%d", candidateCount),
		},
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"reduce %s's %s request, add a larger node to the candidate pool, or re-target the workload's nodeSelector/affinity to a pool with more %s; the correct fix depends on the cluster's capacity plan",
				workloadRef, key, key,
			),
			Commands: []string{
				"kubectl get nodes -o custom-columns=NAME:.metadata.name,CPU:.status.allocatable.cpu,MEM:.status.allocatable.memory",
				"kubectl get nodes --show-labels",
			},
			ContextDependent: true,
		},
		Resource: resourceRef,
	}
}
