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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// LimitRangeFeasibilityCheckID is the stable identifier for this check.
const LimitRangeFeasibilityCheckID = "limitrange-feasibility"

// LimitRangeFeasibility checks whether every container (regular and init) in
// the pod template can actually be admitted against every "Container"-type
// item of every LimitRange in the namespace. Unlike resource-limits (a
// static best-practice check with no cluster read), this is a fact
// verifiable against live cluster state: a namespace LimitRange can turn a
// missing or out-of-range request/limit into a guaranteed pod-creation
// rejection, long before `watch` would see anything.
//
// The most important fact this check relies on, verified live on kind
// (v1.37.0), not only from source: a LimitRangeItem's default/defaultRequest
// fields are backfilled by the API SERVER when the LimitRange object itself
// is created/updated — before any Pod is ever evaluated against it. An item
// declaring only max.cpu: 200m, once read back, already carries
// default.cpu: 200m AND defaultRequest.cpu: 200m; an item declaring only
// min.cpu: 100m already carries defaultRequest.cpu: 100m but no default
// entry at all (nothing to derive a limit from). This is
// SetDefaults_LimitRangeItem (k8s.io/kubernetes@v1.36.1/pkg/apis/core/v1/
// defaults.go:360-389): for a Container-type item, max backfills default
// (if unset), then default backfills defaultRequest (if unset), then min
// backfills defaultRequest (if still unset). The consequence for this check
// is deliberate: it never simulates "max present but no default" as a
// distinct case — a live LimitRange object never has that shape — it only
// reads whatever default/defaultRequest values are already present.
// Verified live: a container declaring nothing at all, under a LimitRange
// with only max.cpu: 200m, gets a real pod with limits.cpu=200m AND
// requests.cpu=200m — creation succeeds, no "no limit is specified"
// rejection is ever reachable for a live object.
//
// Cross-object combination is additive and was verified live with a
// permanent, guaranteed rejection: LimitRange A declares only max.cpu:
// 200m (backfilling default.cpu=200m, defaultRequest.cpu=200m); LimitRange
// B declares only min.cpu: 300m (backfilling defaultRequest.cpu=300m, no
// default entry). A container declaring nothing is rejected forever with
// (verbatim, one of the two orders observed):
//
//	pods "..." is forbidden: minimum cpu usage per Container is 300m,
//	but request is 200m
//
// No read order of A/B avoids it: the other order fails too, with a
// different message ("maximum cpu usage per Container is 200m, but request
// is 300m"), because the LimitRanger admission plugin
// (k8s.io/kubernetes@v1.36.1/plugin/pkg/admission/limitranger/admission.go)
// fills default/defaultRequest per LimitRange object in list order
// (mergeContainerResources, admission.go:236-270 — a key is only filled if
// still absent, so whichever object the apiserver's informer lists first
// for a given field wins that field independently), then validates the
// FINAL per-pod values against every item's own min/max/maxLimitRequestRatio
// (minConstraint/maxConstraint/limitRequestRatioConstraint,
// admission.go:308-385 — note minConstraint also rejects a present LIMIT
// below the minimum, and maxConstraint also rejects a present REQUEST above
// the maximum, not only the request/limit the constraint is named after).
// Enforcing every item's own threshold independently is mathematically
// equivalent to enforcing the single most restrictive combination across
// all items (max of all declared minimums, min of all declared maximums and
// ratios), which is what this check computes as the "combined" bound per
// resource key. Constraints apply identically to regular and init
// containers (PodValidateLimitFunc, admission.go:493-529, byte-for-byte the
// same per-container logic run twice).
//
// Known simplification, declared rather than hidden: when two or more
// LimitRange objects each supply a default/defaultRequest for the SAME
// resource key, the real winner-per-field is coupled (the same object list
// order decides both fields together, see admission.go:274-292), so not
// every (limit-candidate, request-candidate) pairing this check evaluates
// is necessarily reachable by every list order. This check evaluates the
// full cross-product of independently-collected limit/request candidates
// anyway (a superset of the real reachable pairs) rather than simulating
// every real object permutation. This is safe in the High direction (if
// every pair in the superset violates, every real reachable pair violates
// too) but can understate a real "every order fails" case to Medium when a
// pairing outside the real reachable set happens not to violate. Given the
// project's default of declaring a limitation rather than silently getting
// it wrong, this is called out here explicitly rather than claiming exact
// simulation.
//
// Also verified only from source, not reproduced live in this session (the
// cluster available was unstable): the stage-1 requests-from-limits copy
// (SetDefaults_Pod, k8s.io/kubernetes@v1.36.1/pkg/apis/core/v1/
// defaults.go:164-192 — for every key present in a container's declared
// limits but absent from its declared requests, the real Pod object gets
// requests[key] = limits[key], for both regular and init containers,
// BEFORE any admission plugin including LimitRanger ever runs); and
// maxLimitRequestRatio (limitRequestRatioConstraint, admission.go:359-385 —
// requires both an effective request and limit present and nonzero for
// that key, then requires limit/request <= the enforced ratio). This check
// simulates the stage-1 copy on the pod template before consulting any
// LimitRange, and reimplements the ratio comparison with plain float64
// arithmetic rather than admission.go's millivalue-overflow-guarded
// version — irrelevant at the scale of CPU/memory quantities workloads
// declare.
//
// Scope, deliberately narrow for v1: only LimitRangeItem entries of Type
// "Container" are evaluated. Type "Pod" and "PersistentVolumeClaim" items
// are ignored — not an oversight, a decided v1 scope boundary (see the
// planning notes for this check). A LimitRange object cannot declare two
// items of the same Type (validated in admission as a field.Duplicate,
// k8s.io/kubernetes@v1.36.1/pkg/apis/core/validation/validation.go:7623-7638),
// so at most one Container-type item exists per object, but a namespace can
// have several LimitRange objects each contributing one. spec.resources at
// the Pod level (the PodLevelResources feature gate) is not observable from
// outside the cluster and is not considered.
//
// Cross-reference: resource-limits (limits.go) flags a missing limit at Low
// severity as a static best practice, with no cluster read at all. This
// check is the cluster-aware complement: the very same missing limit can be
// a guaranteed, High-severity pod-creation rejection if the namespace has a
// LimitRange that requires one.
type LimitRangeFeasibility struct{}

// ID implements check.Check.
func (LimitRangeFeasibility) ID() string { return LimitRangeFeasibilityCheckID }

// Run implements check.Check.
func (c LimitRangeFeasibility) Run(ctx context.Context, target Target) (Result, error) {
	list, err := target.Client.CoreV1().LimitRanges(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("LimitRange list is not accessible: %v", err)), nil
	}

	items := containerLimitItems(list.Items)
	if len(items) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	objectNames := limitRangeObjectNames(items)
	keys := relevantLimitRangeKeys(items)

	var findings []model.Finding
	for _, container := range target.Workload.PodContainers() {
		findings = append(findings, c.evaluateContainer(target, container, "container", items, keys, objectNames)...)
	}
	for _, container := range target.Workload.InitContainers() {
		findings = append(findings, c.evaluateContainer(target, container, "init container", items, keys, objectNames)...)
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// containerLimitItem pairs a Container-type LimitRangeItem with the name of
// the LimitRange object it came from, since a namespace can have several
// LimitRange objects each contributing constraints that combine additively.
type containerLimitItem struct {
	object string
	item   corev1.LimitRangeItem
}

// containerLimitItems extracts only Type=Container items from every
// LimitRange in the namespace. Type=Pod and Type=PersistentVolumeClaim
// items are out of scope for v1 (see the doc comment on
// LimitRangeFeasibility) and are silently ignored here, not treated as an
// error.
func containerLimitItems(ranges []corev1.LimitRange) []containerLimitItem {
	var out []containerLimitItem
	for _, lr := range ranges {
		for _, item := range lr.Spec.Limits {
			if item.Type != corev1.LimitTypeContainer {
				continue
			}
			out = append(out, containerLimitItem{object: lr.Name, item: item})
		}
	}
	return out
}

// limitRangeObjectNames returns the sorted, de-duplicated names of the
// LimitRange objects that contributed at least one Container-type item,
// used in finding text and the read-only remediation command.
func limitRangeObjectNames(items []containerLimitItem) []string {
	seen := map[string]bool{}
	var names []string
	for _, ci := range items {
		if seen[ci.object] {
			continue
		}
		seen[ci.object] = true
		names = append(names, ci.object)
	}
	sort.Strings(names)
	return names
}

// relevantLimitRangeKeys returns the sorted union of every resource key
// mentioned by any Min/Max/Default/DefaultRequest/MaxLimitRequestRatio
// across all Container-type items: this check is not hardcoded to
// cpu/memory, it evaluates whatever keys the namespace's LimitRange objects
// actually declare.
func relevantLimitRangeKeys(items []containerLimitItem) []corev1.ResourceName {
	seen := map[corev1.ResourceName]bool{}
	add := func(list corev1.ResourceList) {
		for k := range list {
			seen[k] = true
		}
	}
	for _, ci := range items {
		add(ci.item.Min)
		add(ci.item.Max)
		add(ci.item.Default)
		add(ci.item.DefaultRequest)
		add(ci.item.MaxLimitRequestRatio)
	}
	keys := make([]corev1.ResourceName, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// combinedBound reduces a per-item resource.Quantity field (Min, Max, or
// MaxLimitRequestRatio) across every item to the single most restrictive
// value for the given key, using pick to decide which of two candidate
// values is more restrictive. ok is false when no item declares this key
// for this field at all.
func combinedBound(items []containerLimitItem, key corev1.ResourceName, field func(corev1.LimitRangeItem) corev1.ResourceList, pick func(current, candidate resource.Quantity) resource.Quantity) (resource.Quantity, bool) {
	var result resource.Quantity
	found := false
	for _, ci := range items {
		v, ok := field(ci.item)[key]
		if !ok {
			continue
		}
		if !found {
			result = v
			found = true
			continue
		}
		result = pick(result, v)
	}
	return result, found
}

// mostRestrictiveMin keeps the larger of two minimums: a value must satisfy
// every item's own minimum, so the largest declared minimum is the binding
// one.
func mostRestrictiveMin(current, candidate resource.Quantity) resource.Quantity {
	if candidate.Cmp(current) > 0 {
		return candidate
	}
	return current
}

// mostRestrictiveMax keeps the smaller of two maximums (or ratios): a value
// must satisfy every item's own maximum, so the smallest declared maximum
// is the binding one.
func mostRestrictiveMax(current, candidate resource.Quantity) resource.Quantity {
	if candidate.Cmp(current) < 0 {
		return candidate
	}
	return current
}

func combinedMin(items []containerLimitItem, key corev1.ResourceName) (resource.Quantity, bool) {
	return combinedBound(items, key, func(i corev1.LimitRangeItem) corev1.ResourceList { return i.Min }, mostRestrictiveMin)
}

func combinedMax(items []containerLimitItem, key corev1.ResourceName) (resource.Quantity, bool) {
	return combinedBound(items, key, func(i corev1.LimitRangeItem) corev1.ResourceList { return i.Max }, mostRestrictiveMax)
}

func combinedRatio(items []containerLimitItem, key corev1.ResourceName) (resource.Quantity, bool) {
	return combinedBound(items, key, func(i corev1.LimitRangeItem) corev1.ResourceList { return i.MaxLimitRequestRatio }, mostRestrictiveMax)
}

// valueCandidate represents one possible effective value for a container's
// request or limit on a given resource key: present is false when no
// manifest declaration and no LimitRange default/defaultRequest ever
// supplies a value at all.
type valueCandidate struct {
	present bool
	qty     resource.Quantity
}

// distinctLimitRangeValues collects the distinct values (by numeric
// comparison, not string form) that field returns for key across every
// item, sorted ascending for deterministic output.
func distinctLimitRangeValues(items []containerLimitItem, key corev1.ResourceName, field func(corev1.LimitRangeItem) corev1.ResourceList) []resource.Quantity {
	var out []resource.Quantity
	for _, ci := range items {
		v, ok := field(ci.item)[key]
		if !ok {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing.Cmp(v) == 0 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cmp(out[j]) < 0 })
	return out
}

func candidatesFromDistinct(distinct []resource.Quantity) []valueCandidate {
	if len(distinct) == 0 {
		return []valueCandidate{{present: false}}
	}
	out := make([]valueCandidate, len(distinct))
	for i, v := range distinct {
		out[i] = valueCandidate{present: true, qty: v}
	}
	return out
}

// limitCandidates returns the possible effective limit values for this
// container on this resource key: a single, fixed candidate if the
// manifest declares the limit directly, otherwise every distinct
// default[key] value backfilled onto a Container-type item in this
// namespace (zero, one, or several — several only when more than one
// LimitRange object supplies a different default for the same key, an
// order-dependent outcome, see the doc comment on LimitRangeFeasibility).
func limitCandidates(container corev1.Container, items []containerLimitItem, key corev1.ResourceName) []valueCandidate {
	if v, ok := container.Resources.Limits[key]; ok {
		return []valueCandidate{{present: true, qty: v}}
	}
	return candidatesFromDistinct(distinctLimitRangeValues(items, key, func(i corev1.LimitRangeItem) corev1.ResourceList { return i.Default }))
}

// requestCandidates mirrors limitCandidates for the effective request
// value, but also simulates the stage-1 requests-from-limits copy
// (SetDefaults_Pod, see the doc comment on LimitRangeFeasibility): if the
// manifest declares the limit but not the request for this key, the real
// Pod object always gets request=limit before any LimitRange is ever
// consulted, so that is a single, fixed candidate too, not defaulted by any
// LimitRange.
func requestCandidates(container corev1.Container, items []containerLimitItem, key corev1.ResourceName) []valueCandidate {
	if v, ok := container.Resources.Requests[key]; ok {
		return []valueCandidate{{present: true, qty: v}}
	}
	if v, ok := container.Resources.Limits[key]; ok {
		return []valueCandidate{{present: true, qty: v}}
	}
	return candidatesFromDistinct(distinctLimitRangeValues(items, key, func(i corev1.LimitRangeItem) corev1.ResourceList { return i.DefaultRequest }))
}

func anyPresent(candidates []valueCandidate) bool {
	for _, c := range candidates {
		if c.present {
			return true
		}
	}
	return false
}

// evaluateCombo checks one (limit, request) candidate pair against the
// combined min/max/maxLimitRequestRatio bounds, mirroring
// minConstraint/maxConstraint/limitRequestRatioConstraint (see the doc
// comment on LimitRangeFeasibility for source references): a minimum also
// rejects a present limit below it, and a maximum also rejects a present
// request above it, matching the vendored functions exactly.
func evaluateCombo(limit, request valueCandidate, min, max, ratio *resource.Quantity) (bool, string) {
	if min != nil {
		switch {
		case !request.present:
			return true, fmt.Sprintf("minimum value is %s, but no request would be present", min.String())
		case request.qty.Cmp(*min) < 0:
			return true, fmt.Sprintf("minimum value is %s, but request would be %s", min.String(), request.qty.String())
		case limit.present && limit.qty.Cmp(*min) < 0:
			return true, fmt.Sprintf("minimum value is %s, but limit would be %s", min.String(), limit.qty.String())
		}
	}
	if max != nil {
		switch {
		case !limit.present:
			return true, fmt.Sprintf("maximum value is %s, but no limit would be present", max.String())
		case limit.qty.Cmp(*max) > 0:
			return true, fmt.Sprintf("maximum value is %s, but limit would be %s", max.String(), limit.qty.String())
		case request.present && request.qty.Cmp(*max) > 0:
			return true, fmt.Sprintf("maximum value is %s, but request would be %s", max.String(), request.qty.String())
		}
	}
	if ratio != nil {
		switch {
		case !request.present || request.qty.IsZero():
			return true, fmt.Sprintf("max limit/request ratio is %s, but no request would be present or it would be zero", ratio.String())
		case !limit.present || limit.qty.IsZero():
			return true, fmt.Sprintf("max limit/request ratio is %s, but no limit would be present or it would be zero", ratio.String())
		default:
			observed := float64(limit.qty.MilliValue()) / float64(request.qty.MilliValue())
			allowed := float64(ratio.MilliValue()) / 1000.0
			if observed > allowed {
				return true, fmt.Sprintf("max limit/request ratio is %s, but it would be %.2f", ratio.String(), observed)
			}
		}
	}
	return false, ""
}

// evaluateContainer checks one container (regular or init) against every
// relevant resource key, returning High/Medium findings for keys with a
// guaranteed or order-dependent violation and at most one Low finding
// aggregating every resource that received a silent default (LimitRange or
// stage-1 copy) without ever violating anything.
func (c LimitRangeFeasibility) evaluateContainer(target Target, container corev1.Container, kind string, items []containerLimitItem, keys []corev1.ResourceName, objectNames []string) []model.Finding {
	var findings []model.Finding
	var defaultedResources []string

	resourceRef := model.ResourceRef{
		Kind:      "Pod",
		Namespace: target.Namespace,
		Name:      fmt.Sprintf("%s/%s", target.Workload.Name(), container.Name),
	}
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	for _, key := range keys {
		minBound, minOk := combinedMin(items, key)
		maxBound, maxOk := combinedMax(items, key)
		ratioBound, ratioOk := combinedRatio(items, key)
		if !minOk && !maxOk && !ratioOk {
			continue
		}
		var minP, maxP, ratioP *resource.Quantity
		if minOk {
			minP = &minBound
		}
		if maxOk {
			maxP = &maxBound
		}
		if ratioOk {
			ratioP = &ratioBound
		}

		_, limitDeclared := container.Resources.Limits[key]
		_, requestDeclared := container.Resources.Requests[key]

		limitCands := limitCandidates(container, items, key)
		reqCands := requestCandidates(container, items, key)

		total, violating := 0, 0
		var reasons []string
		for _, l := range limitCands {
			for _, r := range reqCands {
				total++
				if bad, reason := evaluateCombo(l, r, minP, maxP, ratioP); bad {
					violating++
					if !containsString(reasons, reason) {
						reasons = append(reasons, reason)
					}
				}
			}
		}

		if violating == 0 {
			limitDefaulted := !limitDeclared && anyPresent(limitCands)
			requestDefaulted := !requestDeclared && anyPresent(reqCands)
			if limitDefaulted || requestDefaulted {
				defaultedResources = append(defaultedResources, string(key))
			}
			continue
		}

		nondeterministic := total > 1 && violating < total
		severity := model.SeverityHigh
		note := ""
		if nondeterministic {
			severity = model.SeverityMedium
			note = fmt.Sprintf(
				" the outcome is non-deterministic: it depends on the order in which the API server lists the LimitRange objects (%s) in this namespace — only %d of %d possible default combinations violate the constraint",
				strings.Join(objectNames, ", "), violating, total,
			)
		}

		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: severity,
			Cause: fmt.Sprintf(
				"%s %q in the pod template of workload %s can be admitted with a %s value that violates a LimitRange constraint from namespace %q object(s) %s: %s.%s",
				kind, container.Name, workloadRef, key, target.Namespace, strings.Join(objectNames, ", "), strings.Join(reasons, "; "), note,
			),
			Evidence: append([]string{
				fmt.Sprintf("resource=%s", key),
				fmt.Sprintf("limitRanges=%s", strings.Join(objectNames, ",")),
			}, reasons...),
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"inspect the LimitRange objects in namespace %q and either adjust %s %q's declared requests/limits for %s or the namespace's min/max/default policy; the correct fix depends on whether the workload manifest or the namespace policy is wrong",
					target.Namespace, kind, container.Name, key,
				),
				Commands:         []string{fmt.Sprintf("kubectl get limitrange -n %s -o yaml", target.Namespace)},
				ContextDependent: true,
			},
			Resource: resourceRef,
		})
	}

	if len(defaultedResources) > 0 {
		sort.Strings(defaultedResources)
		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: model.SeverityLow,
			Cause: fmt.Sprintf(
				"%s %q in the pod template of workload %s does not declare %s explicitly; its effective value comes entirely from Kubernetes' own request/limit copy or from a LimitRange default in namespace %q (%s)",
				kind, container.Name, workloadRef, strings.Join(defaultedResources, ", "), target.Namespace, strings.Join(objectNames, ", "),
			),
			Evidence: []string{fmt.Sprintf("defaultedResources=%s", strings.Join(defaultedResources, ","))},
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"declare explicit requests/limits for %s on %s %q instead of relying on this namespace's LimitRange defaults, which can change independently of this workload's manifest",
					strings.Join(defaultedResources, ", "), kind, container.Name,
				),
				ContextDependent: true,
			},
			Resource: resourceRef,
		})
	}

	return findings
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
