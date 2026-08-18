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

// Package check implements the static pre-flight checks run by
// `kubectl safe-rollout check`. Each check lives in its own file and
// implements the Check interface: this allows new checks to be added
// without changing existing ones and to be tested in isolation with
// client-go/kubernetes/fake.
package check

import (
	"context"

	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// Target groups everything a check may need to read. It is built once
// per `check` invocation and shared among all checks, so the cost of
// List calls (PDB, ResourceQuota, ...) can be amortized upstream if
// needed.
//
// MetricsClient is nil when metrics-server is unreachable: checks that
// need it must degrade to Result.Skipped, never fail the entire `check`
// execution.
type Target struct {
	Namespace     string
	Workload      workload.Workload
	Client        kubernetes.Interface
	MetricsClient metricsv1beta1.MetricsV1beta1Interface
}

// Result is the outcome of running a single Check. Skipped distinguishes
// "no problem found" (empty Findings, Skipped false) from "unable to
// evaluate" (Skipped true, populated SkipReason): conflating them would
// produce silent false negatives, for example when RBAC permissions on
// ResourceQuota are missing.
type Result struct {
	CheckID    string
	Findings   []model.Finding
	Skipped    bool
	SkipReason string
}

// Check is the interface implemented by every pre-flight check. ID must
// remain stable across versions: it is the key used in JSON output and
// for CI gating on individual rules.
type Check interface {
	ID() string
	Run(ctx context.Context, target Target) (Result, error)
}

// Skip builds a Result that explicitly declares that the check cannot
// be evaluated, instead of an empty Result that would be indistinguishable
// from "no problem".
func Skip(id, reason string) Result {
	return Result{CheckID: id, Skipped: true, SkipReason: reason}
}
