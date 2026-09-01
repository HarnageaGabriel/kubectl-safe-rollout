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

// PausedDiagnoserID is the stable category identifier, used as
// Result.DiagnoserID.
const PausedDiagnoserID = "rollout-paused"

// Paused reports when the workload's controller has been told to take no
// action at all (spec.paused). Discovered by running `watch` against a
// paused Deployment on kind: with no readinessProbe race, no ImagePullBackOff,
// nothing for any other Diagnoser to see, and progressDeadlineSeconds itself
// frozen by Kubernetes while paused, watch waited forever with no output and
// no way to explain why. Structured signal, no pattern to interpret, so
// there is no "-undetermined" variant, matching ProgressDeadline.
type Paused struct{}

// ID implements Diagnoser.
func (Paused) ID() string { return PausedDiagnoserID }

// Diagnose implements Diagnoser.
func (Paused) Diagnose(_ context.Context, target Target) (Result, error) {
	if target.Workload.Kind() == "StatefulSet" {
		// StatefulSet simply has no spec.paused field, full stop: unlike
		// ProgressDeadlineExceeded's ok=false, Workload.Paused()'s bare
		// false return here carries no ambiguity to preserve, but a silent
		// empty Result would still look identical to "evaluated and found
		// not paused" rather than "cannot evaluate this category for this
		// kind" (see rules/output.md on Skipped visibility).
		return SkipResult(PausedDiagnoserID, "StatefulSet has no spec.paused field: this category cannot be evaluated for this kind"), nil
	}
	if !target.Workload.Paused() {
		return Result{DiagnoserID: PausedDiagnoserID}, nil
	}

	resource := model.ResourceRef{
		Kind:      target.Workload.Kind(),
		Namespace: target.Workload.Namespace(),
		Name:      target.Workload.Name(),
	}
	finding := model.Finding{
		CheckID:  string(CauseRolloutPaused),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%s is paused: the controller will not create, update, or otherwise progress this rollout until it is resumed",
			resource,
		),
		Evidence: []string{"spec.paused=true"},
		Remediation: model.Remediation{
			// No Commands: resuming is a real state change with real
			// consequences (whatever was paused resumes rolling out), not a
			// read-only command like those in the other Diagnosers. It stays
			// prose so it is not mistaken for something safe to copy-paste,
			// matching how ProgressDeadline handles `kubectl rollout undo`.
			Summary:          "this may be deliberate (e.g. `kubectl rollout pause`, or a tool like Argo Rollouts managing the rollout in stages): confirm before resuming with `kubectl rollout resume`",
			ContextDependent: true,
		},
		Resource: resource,
	}
	return Result{DiagnoserID: PausedDiagnoserID, Findings: []model.Finding{finding}}, nil
}
