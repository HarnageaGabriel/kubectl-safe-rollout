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
)

// ProgressDeadlineDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const ProgressDeadlineDiagnoserID = "progress-deadline"

// ProgressDeadline reports when the workload controller has already
// concluded on its own that the rollout is not progressing within its
// deadline. Unlike the other Diagnosers, there is no pattern to recognize:
// the condition read by Workload.ProgressDeadlineExceeded() is already the
// cause, not a symptom to interpret, so this category has no
// "-undetermined" variant.
//
// StatefulSet has no progressDeadlineSeconds field and no controller-set
// Progressing condition at all (see Workload.ProgressDeadlineExceeded's own
// doc comment): ok=false from that method is therefore ambiguous on its own
// — it also means "applicable to this kind, just not exceeded right now"
// for Deployment. Gating on target.Workload.Kind() directly, instead of
// treating every ok=false as a Skip, keeps that distinction intact: a
// Deployment mid-rollout that has not yet exceeded its deadline must still
// silently report no Finding, not a Skip.
type ProgressDeadline struct{}

// ID implements Diagnoser.
func (ProgressDeadline) ID() string { return ProgressDeadlineDiagnoserID }

// Diagnose implements Diagnoser.
func (ProgressDeadline) Diagnose(_ context.Context, target Target) (Result, error) {
	if target.Workload.Kind() == "StatefulSet" {
		return SkipResult(ProgressDeadlineDiagnoserID, "StatefulSet has no progressDeadlineSeconds and its controller sets no Progressing condition: this category cannot be evaluated for this kind"), nil
	}
	message, exceeded := target.Workload.ProgressDeadlineExceeded()
	if !exceeded {
		return Result{DiagnoserID: ProgressDeadlineDiagnoserID}, nil
	}

	resource := model.ResourceRef{
		Kind:      target.Workload.Kind(),
		Namespace: target.Workload.Namespace(),
		Name:      target.Workload.Name(),
	}
	finding := model.Finding{
		CheckID:  string(CauseProgressDeadlineExceeded),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%s exceeded its progressDeadlineSeconds: the controller concluded that the rollout is not progressing",
			resource,
		),
		Evidence: []string{fmt.Sprintf("condition Progressing/ProgressDeadlineExceeded: %s", message)},
		Remediation: model.Remediation{
			// No Commands: a rollback (`kubectl rollout undo`) is an action
			// with real consequences, not a read-only command like those in
			// the other Diagnosers. Suggesting it as a ready-made command
			// would normalize an action that must remain an explicit operator
			// decision, not a copy-paste operation.
			Summary:          "the other Diagnosers in this command classify this rollout's pods separately; if none reported a cause, inspect the pods directly because the cause is outside what this tool classifies today, or consider a rollback with `kubectl rollout undo` if the previous release was stable",
			ContextDependent: true,
		},
		Resource: resource,
	}
	return Result{DiagnoserID: ProgressDeadlineDiagnoserID, Findings: []model.Finding{finding}}, nil
}
