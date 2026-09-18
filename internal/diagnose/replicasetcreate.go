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

	corev1 "k8s.io/api/core/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ReplicaSetCreateDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const ReplicaSetCreateDiagnoserID = "replicaset-create"

// ReplicaSetCreate classifies a Deployment that cannot create a new
// ReplicaSet for its current pod template — a distinct, and structurally
// invisible-to-Quota, failure mode from the one Quota classifies. Quota
// reads a Reason=="FailedCreate" event on the ReplicaSet the rejected pod
// would have belonged to; here, the ReplicaSet itself was never created, so
// no such child object and no such event ever exist. The signal instead
// lives on the Deployment's own "Progressing" condition
// (Reason=="ReplicaSetCreateError", written by
// deploymentutil.FailedRSCreateReason in k8s.io/kubernetes) or, when that
// condition cannot exist at all (spec.progressDeadlineSeconds disabled via
// the documented math.MaxInt32 sentinel), a Warning event on the Deployment
// itself carrying the same Reason: the controller emits that event
// unconditionally, even when the condition update is gated off. See
// CauseReplicaSetCreateQuotaExceeded/CauseReplicaSetCreateUndetermined in
// cause.go for the full mechanism citation.
//
// Only Deployment has a ReplicaSet-mediated pod-creation path at all:
// StatefulSet and DaemonSet create Pods (and, for their rollout mechanism,
// ControllerRevisions) directly, have no "Progressing" condition type, and
// their own quota-exhaustion equivalent was verified live in a prior
// session to be completely silent — no condition, no event anywhere in the
// namespace (see internal/check/quotaobjectcount.go's doc comment and
// Workload.ReplicaSetCreateError's doc comment on both kinds). This
// Diagnoser Skips for those two kinds instead of silently reporting no
// Finding, the same convention already used by ProgressDeadline/Paused.
//
// Fires immediately on the signal, one-shot, the same posture as Quota and
// ProgressDeadline: no settle/corroboration window, and this ID is
// deliberately not added to settleWindowDiagnoserIDs in watch.go.
type ReplicaSetCreate struct{}

// ID implements Diagnoser.
func (ReplicaSetCreate) ID() string { return ReplicaSetCreateDiagnoserID }

// replicaSetCreateErrorReason mirrors the Reason value read by
// Workload.ReplicaSetCreateError, duplicated here (not exported from
// internal/workload) because it also has to match the fallback Event's
// Reason, which is a Diagnoser-level concern, not a Workload one.
const replicaSetCreateErrorReason = "ReplicaSetCreateError"

// Diagnose implements Diagnoser.
//
// Gate limitation, deliberately accepted: the Workload interface exposes
// only two named "Progressing" reasons (ReplicaSetCreateError itself, and
// ProgressDeadlineExceeded). The fallback event path below is gated on
// "no Progressing condition present at all", approximated as "neither of
// those two accessors currently matches" — not a generic
// "is any Progressing condition present" signal, which would require a
// broader Workload refactor deliberately out of scope for this change (see
// CLAUDE.md/the change that introduced this Diagnoser). This leaves a
// narrow, documented gap: a stale ReplicaSetCreateError event sitting next
// to a Deployment whose Progressing condition has already recovered to a
// third, unexposed reason (e.g. NewReplicaSetAvailable, ReplicaSetUpdated)
// within the event's ~1h TTL will not be suppressed by this specific check.
// The mutual-exclusivity guarantee against ProgressDeadlineExceeded (see
// cause.go) is exact, not approximate: that reason lives on the exact same
// condition and cannot coexist with ReplicaSetCreateError.
func (d ReplicaSetCreate) Diagnose(_ context.Context, target Target) (Result, error) {
	switch target.Workload.Kind() {
	case "StatefulSet", "DaemonSet":
		return SkipResult(ReplicaSetCreateDiagnoserID, fmt.Sprintf(
			"%s creates Pods directly and has no ReplicaSet or Progressing condition: this category cannot be evaluated for this kind",
			target.Workload.Kind(),
		)), nil
	}

	message, ok := target.Workload.ReplicaSetCreateError()
	evidencePrefix := "condition Progressing/ReplicaSetCreateError"
	if !ok {
		if _, deadlineExceeded := target.Workload.ProgressDeadlineExceeded(); deadlineExceeded {
			// A different, known Progressing reason is present: this is not
			// the "no condition at all" case the fallback exists for.
			return Result{DiagnoserID: ReplicaSetCreateDiagnoserID}, nil
		}
		found, count := findReplicaSetCreateErrorEvent(target)
		if found == "" {
			return Result{DiagnoserID: ReplicaSetCreateDiagnoserID}, nil
		}
		message = found
		evidencePrefix = "event"
		if count > 1 {
			evidencePrefix = fmt.Sprintf("event (seen %dx)", count)
		}
	}

	resource := model.ResourceRef{
		Kind:      target.Workload.Kind(),
		Namespace: target.Workload.Namespace(),
		Name:      target.Workload.Name(),
	}
	evidence := []string{fmt.Sprintf("%s: %s", evidencePrefix, message)}

	if pattern.QuotaExceeded(message) {
		finding := model.Finding{
			CheckID:  string(CauseReplicaSetCreateQuotaExceeded),
			Severity: model.SeverityHigh,
			Cause:    fmt.Sprintf("%s cannot create a new ReplicaSet for its current pod template: the namespace ResourceQuota is exhausted", resource),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary: "increase the namespace ResourceQuota's count/replicasets.apps hard limit, or free a slot by deleting an inactive, scaled-down ReplicaSet in the namespace; lowering revisionHistoryLimit will not unblock this rollout right now, since its cleanup only runs once the rollout completes, and it cannot complete while blocked; a rollback to a pod template matching an existing ReplicaSet is unaffected, the controller reuses it instead of creating a new one",
				Commands: []string{
					fmt.Sprintf("kubectl describe deployment %s -n %s", target.Workload.Name(), target.Workload.Namespace()),
					fmt.Sprintf("kubectl get resourcequota -n %s -o yaml", target.Workload.Namespace()),
					fmt.Sprintf("kubectl get replicaset -n %s -o wide", target.Workload.Namespace()),
				},
				ContextDependent: true,
			},
			Resource: resource,
		}
		return Result{DiagnoserID: ReplicaSetCreateDiagnoserID, Findings: []model.Finding{finding}}, nil
	}

	finding := model.Finding{
		CheckID:  string(CauseReplicaSetCreateUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s cannot create a new ReplicaSet for its current pod template, but the message does not mention an exceeded ResourceQuota", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "read the full message: it may be an admission webhook, a ValidatingAdmissionPolicy, or another constraint on ReplicaSet creation",
			Commands:         []string{fmt.Sprintf("kubectl describe deployment %s -n %s", target.Workload.Name(), target.Workload.Namespace())},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
	return Result{DiagnoserID: ReplicaSetCreateDiagnoserID, Findings: []model.Finding{finding}}, nil
}

// findReplicaSetCreateErrorEvent looks up a Warning event on the
// Deployment's own UID with Reason=="ReplicaSetCreateError", reusing
// Target.EventsByUID (already populated once per tick for every Diagnoser,
// see GroupEventsByInvolvedObject): no new API call is made here. Returns
// the empty string if none is found. count is the event's own Count field,
// aggregated by the apiserver when the identical event repeats (unlike the
// per-pod FailedCreate events Quota reads, this message is identical on
// every retry: same ReplicaSet name, same rejection), so unlike Quota there
// is at most one matching Event object to find, not several to collapse.
func findReplicaSetCreateErrorEvent(target Target) (message string, count int32) {
	for _, e := range target.EventsByUID[target.Workload.UID()] {
		if e.Type != corev1.EventTypeWarning || e.Reason != replicaSetCreateErrorReason {
			continue
		}
		return e.Message, e.Count
	}
	return "", 0
}
