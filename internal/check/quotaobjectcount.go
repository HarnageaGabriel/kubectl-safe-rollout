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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// QuotaObjectCountCheckID is the stable identifier for this check.
const QuotaObjectCountCheckID = "quota-object-count"

// quotaObjectCountResource describes, for one supported workload Kind, which
// object-count ResourceQuota key gates the object its controller must create
// before it can even start a rollout, plus how to name that object in
// messages and remediation.
type quotaObjectCountResource struct {
	// key is the exact ResourceQuota key the apiserver's ResourceQuota
	// admission plugin enforces for this GroupResource:
	// ObjectCountQuotaResourceNameFor
	// (k8s.io/apiserver@v0.36.1/pkg/quota/v1/generic/evaluator.go, lines
	// 125-131) builds it as "count/<resource>" when the API group is empty,
	// "count/<resource>.<group>" otherwise. There is no other valid
	// spelling: verified live on kind that a quota keyed "replicasets" (no
	// "count/" prefix) or "count/replicasets" (missing the ".apps" group
	// suffix) enforces nothing at all.
	key string
	// childKind is the Kubernetes Kind consumed by each unit of this quota
	// key (e.g. "ReplicaSet", "ControllerRevision"), used only in
	// human-readable messages and remediation.
	childKind string
	// listCommandFormat is a read-only "%s"-templated (namespace) kubectl
	// command that inventories which objects are consuming the quota's
	// slots, kind-aware on purpose: internal/check/quota.go's remediation
	// once hardcoded a "describe replicaset" command even when the real
	// PodCreationSources was a StatefulSet (see CLAUDE.md's "recurring
	// maintenance note" on internal/diagnose/quota.go); this field exists so
	// the same class of mistake cannot happen here.
	listCommandFormat string
}

// quotaObjectCountResources maps each workload Kind this project supports to
// the object-count ResourceQuota key its rollout mechanism can actually run
// into.
//
// Deployment creates a brand new ReplicaSet for any pod template change:
// count/replicasets.apps.
//
// StatefulSet and DaemonSet both create a new ControllerRevision instead —
// neither has a ReplicaSet-equivalent child object, and both use the same
// history package — so both map to count/controllerrevisions.apps.
//
// A Kind absent from this map (there is none among the three supported
// today, but a future Workload implementation could add one before this
// check catches up) is treated by Run as "not applicable", not as an error:
// see the early return below.
var quotaObjectCountResources = map[string]quotaObjectCountResource{
	"Deployment": {
		key:               "count/replicasets.apps",
		childKind:         "ReplicaSet",
		listCommandFormat: "kubectl get replicaset -n %s -o wide",
	},
	"StatefulSet": {
		key:               "count/controllerrevisions.apps",
		childKind:         "ControllerRevision",
		listCommandFormat: "kubectl get controllerrevision -n %s",
	},
	"DaemonSet": {
		key:               "count/controllerrevisions.apps",
		childKind:         "ControllerRevision",
		listCommandFormat: "kubectl get controllerrevision -n %s",
	},
}

// QuotaObjectCount checks whether a namespace ResourceQuota that constrains
// the object-count key for this workload's rollout mechanism (ReplicaSet for
// Deployment, ControllerRevision for StatefulSet/DaemonSet) has already run
// out of headroom for one more object. quota-headroom already checks
// headroom for the pods and cpu/memory a rolling-update surge consumes; this
// is a distinct resource entirely — the bookkeeping object the controller
// itself must create before it creates or updates a single Pod — invisible
// to that check's calculation.
//
// Verified live on kind (v1.37.0) in this session:
//
//   - Deployment: with count/replicasets.apps hard=used=1 (one existing
//     ReplicaSet already consuming the only slot), a `kubectl set image`
//     (a genuine pod template change) never creates the new ReplicaSet at
//     all. The Deployment's "Progressing" condition flips to
//     Status=False, Reason=ReplicaSetCreateError, with this verbatim
//     message:
//     Failed to create new replica set "victim-8ff854b9f": replicasets.apps
//     "victim-8ff854b9f" is forbidden: exceeded quota: rs-quota, requested:
//     count/replicasets.apps=1, used: count/replicasets.apps=1, limited:
//     count/replicasets.apps=1
//     The event lands on the Deployment itself (involvedObject.kind=Deployment)
//     with Reason=ReplicaSetCreateError (Warning), never FailedCreate — and
//     internal/diagnose/quota.go filters exclusively on
//     Reason=="FailedCreate" over Target.PodCreationSources. `watch` is
//     therefore structurally blind to this failure: wrong reason, wrong
//     object, and the object that would carry it (the new ReplicaSet) never
//     comes into existence for `watch` to observe.
//   - Rollback control, verified with the same quota still fully exhausted:
//     rolling back to the previous pod template (the one the existing,
//     already-counted ReplicaSet matches) succeeds —
//     Reason=NewReplicaSetAvailable, Status=True. The controller reuses the
//     matching ReplicaSet (FindNewReplicaSet) instead of creating a new
//     one, so it consumes zero additional quota. This is why the Cause
//     below is careful to say the *next* rollout that changes the pod
//     template is blocked, never "every rollout" — a rollback to a
//     template already in history is unaffected.
//   - StatefulSet: with count/controllerrevisions.apps hard=used=1 (one
//     existing ControllerRevision), a `kubectl set image` on the
//     StatefulSet is rejected completely and silently: no Warning event is
//     emitted anywhere in the namespace (checked by listing every event),
//     status.updateRevision never diverges from status.currentRevision (no
//     new revision is ever attempted or recorded), and the pod keeps
//     running the old image indefinitely. This is even more invisible than
//     the Deployment case (which at least gets a Warning event and a
//     condition flip) — it is the strongest justification for this check
//     existing for StatefulSet in particular.
//   - DaemonSet: NOT reproduced live in this session (the kind cluster used
//     to verify the two cases above crashed before DaemonSet could be
//     exercised — environment instability unrelated to this project,
//     already documented elsewhere in CLAUDE.md). Verified only by reading
//     vendored source (not a dependency of this module, read from a local
//     module cache that happened to have it from prior work): syncDaemonSet
//     calls constructHistory (which creates a ControllerRevision when
//     needed) and returns immediately on error, before ever calling
//     updateDaemonSet — so the blockage should be total (no update, no
//     scale) exactly like StatefulSet, but this specific claim rests on
//     source reading alone, not live reproduction.
//
// Deliberately out of scope:
//
//   - count/deployments.apps, count/statefulsets.apps, count/daemonsets.apps:
//     these would block creating the workload object itself, but by the
//     time this tool runs the workload already exists.
//   - count/pods: a distinct ResourceQuota key from the plain "pods" key
//     quota-headroom already reads — count/pods additionally counts
//     Succeeded/Failed pods still in etcd, not only active ones. Left as
//     future scope for quota-headroom (the concept it already owns), not
//     duplicated here under a second CheckID.
//   - count/configmaps, count/secrets: an ordinary rollout does not create
//     either.
//
// Scoped quotas (spec.scopes or spec.scopeSelector non-empty) are skipped
// entirely, not treated as applicable. This was verified from source, not
// assumed: generic.Matches
// (k8s.io/apiserver@v0.36.1/pkg/quota/v1/generic/evaluator.go, lines
// 150-169) starts matchScope at true and, for every scope selector attached
// to the quota, ANDs it with the evaluator's scopeFunc result — and
// objectCountEvaluator.Matches (same file, lines 260-263) always passes
// MatchesNoScopeFunc, which unconditionally returns false. So a ResourceQuota
// with any non-empty scope can never match an object-count evaluation at
// all, regardless of which resource key it declares: the apiserver itself
// never applies it here. This is the opposite posture from quota-headroom's
// documented choice (which treats a scoped quota as if it still applied, to
// avoid overestimating headroom) — that check's resources (pods, cpu,
// memory requests) are matched by a different evaluator, not the
// object-count one this check reads, so the two checks are not
// inconsistent with each other, only reading different mechanisms.
//
// Deliberate omission: this check never lists the ReplicaSets/
// ControllerRevisions that actually occupy the slots — only ResourceQuota is
// read. The user gets that inventory from the read-only kubectl command in
// the Remediation instead; listing them here would add API cost this v1
// does not need.
//
// Follow-up, not built here: a reactive diagnoser in internal/diagnose (for
// example "replicaset-create-error") that classifies the
// Reason=ReplicaSetCreateError condition/event captured verbatim above, the
// same way internal/diagnose/quota.go already classifies FailedCreate. Left
// for a future change.
type QuotaObjectCount struct{}

// ID implements check.Check.
func (QuotaObjectCount) ID() string { return QuotaObjectCountCheckID }

// Run implements check.Check.
func (c QuotaObjectCount) Run(ctx context.Context, target Target) (Result, error) {
	quotaList, err := target.Client.CoreV1().ResourceQuotas(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("ResourceQuota list is not accessible: %v", err)), nil
	}
	if len(quotaList.Items) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	res, ok := quotaObjectCountResources[target.Workload.Kind()]
	if !ok {
		return Result{CheckID: c.ID()}, nil
	}

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	key := corev1.ResourceName(res.key)

	var findings []model.Finding
	for _, q := range quotaList.Items {
		if quotaHasScope(q) {
			// Never matched by the object-count evaluator (see doc comment
			// above): evaluating it here would be a pure false positive the
			// apiserver itself would never produce.
			continue
		}

		hard, hardExists := q.Status.Hard[key]
		if !hardExists {
			// This quota does not constrain the resource this workload's
			// rollout mechanism actually consumes.
			continue
		}

		used, usedExists := q.Status.Used[key]
		if !usedExists {
			// Hard declares the key but Used has not reported it yet: the
			// quota controller has not synced this ResourceQuota's status.
			// Treating an absent Used as zero would silently assume full
			// headroom exists — exactly the false negative this check
			// exists to avoid. Skip explicitly instead of guessing.
			return Skip(c.ID(), fmt.Sprintf(
				"ResourceQuota %q declares a hard limit for %s but its status has not reported a used count yet (quota controller not synced)",
				q.Name, res.key,
			)), nil
		}

		if used.Cmp(hard) < 0 {
			continue
		}

		findings = append(findings, quotaObjectCountFinding(q, res, workloadRef, target.Namespace, hard, used))
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// quotaHasScope reports whether a ResourceQuota declares a non-empty
// spec.scopes or spec.scopeSelector: see the doc comment on QuotaObjectCount
// for why such a quota never applies to an object-count evaluation at all.
func quotaHasScope(q corev1.ResourceQuota) bool {
	if len(q.Spec.Scopes) > 0 {
		return true
	}
	return q.Spec.ScopeSelector != nil && len(q.Spec.ScopeSelector.MatchExpressions) > 0
}

func quotaObjectCountFinding(q corev1.ResourceQuota, res quotaObjectCountResource, workloadRef, namespace string, hard, used resource.Quantity) model.Finding {
	var blockedAction, revisionHistoryCaveat string
	if res.childKind == "ReplicaSet" {
		blockedAction = fmt.Sprintf("the next rollout of %s that changes the pod template will not be able to start: its controller will fail to create the new ReplicaSet", workloadRef)
		revisionHistoryCaveat = fmt.Sprintf("lowering revisionHistoryLimit on %s will not free a slot right now: its cleanup only runs once the current rollout completes, and this rollout cannot complete while blocked", workloadRef)
	} else {
		blockedAction = fmt.Sprintf("the next update of %s will not even be able to start: its controller creates a new ControllerRevision before it does anything else, and that creation will be rejected", workloadRef)
		revisionHistoryCaveat = fmt.Sprintf("lowering revisionHistoryLimit on %s may not free a slot immediately either, since revision cleanup in Kubernetes controllers typically only runs once a sync completes successfully", workloadRef)
	}

	return model.Finding{
		CheckID:  QuotaObjectCountCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"ResourceQuota %q has no headroom left for %s (hard=%s, used=%s): %s. A rollback to a pod template matching an existing %s is unaffected — the controller reuses it instead of creating a new one.",
			q.Name, res.key, hard.String(), used.String(), blockedAction, res.childKind,
		),
		Evidence: []string{
			fmt.Sprintf("quota %s hard=%s used=%s", res.key, hard.String(), used.String()),
		},
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"increase %q's hard[%s] limit, or free a slot by deleting an inactive, scaled-down %s in the namespace; %s — delete one manually instead, or raise the quota",
				q.Name, res.key, res.childKind, revisionHistoryCaveat,
			),
			Commands: []string{
				fmt.Sprintf("kubectl get resourcequota %s -n %s -o yaml", q.Name, namespace),
				fmt.Sprintf(res.listCommandFormat, namespace),
			},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{Kind: "ResourceQuota", Namespace: q.Namespace, Name: q.Name},
	}
}
