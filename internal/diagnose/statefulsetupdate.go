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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// StatefulSetUpdateDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const StatefulSetUpdateDiagnoserID = "statefulset-update"

// StatefulSetUpdate reports two StatefulSet-specific states where the
// controller has a pending update (Status.UpdateRevision differs from
// Status.CurrentRevision) but will never act on its own to complete it:
// OnDelete strategy, and a RollingUpdate partition that excludes every pod.
// Both are structured signals read directly from Spec/Status, not text
// patterns to interpret, so neither has an "-undetermined" variant, matching
// ProgressDeadline and Paused. No-op for any workload where
// Workload.Kind() != "StatefulSet" (Deployment's PendingRevisionUpdate
// always reports ok=false, so the guard below is defense in depth, not the
// only thing preventing a Deployment finding).
type StatefulSetUpdate struct{}

// ID implements Diagnoser.
func (StatefulSetUpdate) ID() string { return StatefulSetUpdateDiagnoserID }

// Diagnose implements Diagnoser.
func (d StatefulSetUpdate) Diagnose(_ context.Context, target Target) (Result, error) {
	if target.Workload == nil || target.Workload.Kind() != "StatefulSet" {
		return Result{DiagnoserID: d.ID()}, nil
	}
	updateRevision, currentRevision, ok := target.Workload.PendingRevisionUpdate()
	if !ok || updateRevision == "" || updateRevision == currentRevision {
		// No pending update: either the concept does not apply, or the
		// current and desired revisions already match.
		return Result{DiagnoserID: d.ID()}, nil
	}

	resource := model.ResourceRef{
		Kind:      target.Workload.Kind(),
		Namespace: target.Workload.Namespace(),
		Name:      target.Workload.Name(),
	}
	strategy := target.Workload.UpdateStrategy()

	if strategy.Type == workload.OnDelete {
		finding := model.Finding{
			CheckID:  string(CauseStatefulSetUpdateOnDelete),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"%s uses the OnDelete update strategy and has a pending update (updateRevision %q differs from currentRevision %q): the controller will not create, update, or delete any pod on its own until the operator deletes them",
				resource, updateRevision, currentRevision,
			),
			Evidence: []string{
				fmt.Sprintf("status.updateRevision=%s", updateRevision),
				fmt.Sprintf("status.currentRevision=%s", currentRevision),
				"spec.updateStrategy.type=OnDelete",
			},
			Remediation: model.Remediation{
				// No Commands: deleting pods is a real, state-changing action
				// with real consequences on a stateful workload (an
				// in-progress write, a replica falling out of quorum), and
				// OnDelete is frequently a deliberate choice (e.g. an
				// operator or external controller manages pod replacement in
				// a specific order) — same reasoning already applied to
				// rollout-paused.
				Summary: fmt.Sprintf(
					"OnDelete is frequently a deliberate choice (e.g. an operator or external tool manages the rollout in a specific order); if this update should proceed, delete the pods that must move to revision %q yourself, one at a time — this tool never deletes pods on its own",
					updateRevision,
				),
				ContextDependent: true,
			},
			Resource: resource,
		}
		return Result{DiagnoserID: d.ID(), Findings: []model.Finding{finding}}, nil
	}

	// 0 < partition < replicas is the standard canary idiom: only pods with
	// an ordinal >= partition are ever updated, and this is a deliberate,
	// healthy throttle, not a stuck rollout. Only partition >= replicas
	// makes the pending revision mathematically unreachable.
	if strategy.Type == workload.RollingUpdate && strategy.Partition != nil && *strategy.Partition >= target.Workload.Replicas() {
		finding := model.Finding{
			CheckID:  string(CauseStatefulSetPartitionBlocked),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"%s has a pending update (updateRevision %q differs from currentRevision %q) but spec.updateStrategy.rollingUpdate.partition=%d is greater than or equal to %d replicas: no pod ordinal is eligible for the update",
				resource, updateRevision, currentRevision, *strategy.Partition, target.Workload.Replicas(),
			),
			Evidence: []string{
				fmt.Sprintf("status.updateRevision=%s", updateRevision),
				fmt.Sprintf("status.currentRevision=%s", currentRevision),
				fmt.Sprintf("spec.updateStrategy.rollingUpdate.partition=%d", *strategy.Partition),
				fmt.Sprintf("spec.replicas=%d", target.Workload.Replicas()),
			},
			Remediation: model.Remediation{
				// A read-only Command is safe here (it only shows the current
				// value); the fix itself (lowering partition) is a spec edit
				// only the operator can authorize, so it stays prose, not a
				// ready-made mutating command.
				Summary: "lowering spec.updateStrategy.rollingUpdate.partition allows pods with an ordinal at or above the new value to update; the correct value depends on how much of the StatefulSet should move to the new revision now",
				Commands: []string{
					fmt.Sprintf("kubectl get statefulset %s -n %s -o jsonpath='{.spec.updateStrategy.rollingUpdate.partition}'", resource.Name, resource.Namespace),
				},
				ContextDependent: true,
			},
			Resource: resource,
		}
		return Result{DiagnoserID: d.ID(), Findings: []model.Finding{finding}}, nil
	}

	return Result{DiagnoserID: d.ID()}, nil
}
