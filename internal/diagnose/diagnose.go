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

// Package diagnose classifies the causes of failure or stall of a rollout
// observed by `kubectl safe-rollout watch`. Each cause lives in its own file
// and implements the Diagnoser interface, using the same design as
// internal/check: adding a classification does not affect existing ones,
// and each one can be tested in isolation with client-go/kubernetes/fake.
//
// Non-negotiable constraint: classification is deterministic. If the
// collected evidence is insufficient to distinguish among a category's
// known causes, the Diagnoser reports that category's "-undetermined"
// variant (see cause.go), with model.Finding.Undetermined set to true and
// Evidence listing what was observed. Never choose a cause by guessing: on
// a production cluster, a wrong diagnosis costs more than silence.
package diagnose

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// LogTailer retrieves the last log lines from the previous container or init
// container, used as additional evidence for repeated failures. It never
// affects classification, which relies only on structured signals
// (ContainerStatus) and Events: if the fetch fails or LogTailer is nil, the
// Finding remains valid, only with one less evidence line. It is an interface,
// not a concrete client, to remain testable without a real API server.
type LogTailer interface {
	PreviousLogTail(ctx context.Context, namespace, pod, container string, lines int64) (string, error)
}

// Target groups the state of a rollout observed at a given instant,
// already read by the caller (watch.go, once per tick). Diagnosers do not
// perform their own List/Get calls on the cluster except for narrow
// exceptions (e.g. reading a single PersistentVolumeClaim referenced by a
// specific pod): this amortizes the cost of heavier List calls (Events for
// the entire namespace) over one read per tick, shared by all registered
// Diagnosers.
type Target struct {
	Namespace string
	Workload  workload.Workload
	Client    kubernetes.Interface
	// Pods are the workload's current pods, already filtered by label
	// selector by the caller.
	Pods []corev1.Pod
	// ReplicaSets are the ReplicaSets owned by the observed Deployment,
	// required only by the Quota Diagnoser (quota exhaustion events live
	// on the ReplicaSet, not the Pod: the pod never exists).
	ReplicaSets []appsv1.ReplicaSet
	// EventsByUID indexes namespace Events by the UID of the involved
	// object (Pod or ReplicaSet). See
	// GroupEventsByInvolvedObject.
	EventsByUID map[types.UID][]corev1.Event
	// LogTailer may be nil: no additional log evidence; classification
	// does not depend on it.
	LogTailer LogTailer
}

// Result is the outcome of running a single Diagnoser. It deliberately
// mirrors check.Result: Skipped distinguishes "no known cause observed"
// (empty Findings, Skipped false) from "unable to evaluate this category"
// (Skipped true, SkipReason set, typically due to an RBAC error on a
// specific resource such as a PersistentVolumeClaim).
type Result struct {
	DiagnoserID string
	Findings    []model.Finding
	Skipped     bool
	SkipReason  string
}

// Diagnoser is the interface implemented by every cause classification.
// It mirrors check.Check: ID is the stable identifier for the category
// (not the individual cause, see CauseID), Diagnose never writes to the
// cluster and never fails for an expected cluster condition (expressed by
// Result.Skipped; errors are reserved for internal bugs).
type Diagnoser interface {
	ID() string
	Diagnose(ctx context.Context, target Target) (Result, error)
}

// SkipResult builds a Result that explicitly declares the inability to
// evaluate a category, instead of an empty Result indistinguishable from
// "no known cause observed".
func SkipResult(id, reason string) Result {
	return Result{DiagnoserID: id, Skipped: true, SkipReason: reason}
}

// registeredDiagnosers lists all classifications run by RunDiagnosis. It
// is the only place to change when adding a new classified cause.
func registeredDiagnosers() []Diagnoser {
	return []Diagnoser{
		CrashLoop{},
		ImagePull{},
		ConfigError{},
		VolumeMount{},
		InitContainer{},
		Pending{},
		Quota{},
		ProgressDeadline{},
	}
}

// RunDiagnosis runs all registered Diagnosers against the state observed
// in target and returns each Result in registration order. An error from a
// Diagnoser stops the entire diagnosis: it is reserved for internal bugs
// (e.g. selector construction failing because of our bug), not expected
// cluster conditions.
func RunDiagnosis(ctx context.Context, target Target) ([]Result, error) {
	diagnosers := registeredDiagnosers()
	results := make([]Result, 0, len(diagnosers))
	for _, d := range diagnosers {
		res, err := d.Diagnose(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("diagnosis %s: %w", d.ID(), err)
		}
		results = append(results, res)
	}
	return results, nil
}

// GroupEventsByInvolvedObject indexes Events by the UID of the involved
// object (Pod or ReplicaSet), so each Diagnoser accesses relevant events
// with a lookup instead of scanning the namespace's entire Event list for
// every pod.
func GroupEventsByInvolvedObject(events []corev1.Event) map[types.UID][]corev1.Event {
	grouped := make(map[types.UID][]corev1.Event)
	for _, e := range events {
		uid := e.InvolvedObject.UID
		if uid == "" {
			continue
		}
		grouped[uid] = append(grouped[uid], e)
	}
	return grouped
}

// AnyFindings reports whether at least one Result contains a Finding. The
// watch.go loop uses it to decide that the rollout has a diagnosable cause
// and stop observing.
func AnyFindings(results []Result) bool {
	for _, r := range results {
		if len(r.Findings) > 0 {
			return true
		}
	}
	return false
}

// AllFindings flattens Findings from all Results in Result order. The
// caller (watch.go) uses it to build the final Outcome without making
// every caller repeat the same loop.
func AllFindings(results []Result) []model.Finding {
	var findings []model.Finding
	for _, r := range results {
		findings = append(findings, r.Findings...)
	}
	return findings
}
