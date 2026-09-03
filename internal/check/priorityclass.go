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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// PriorityClassExistsCheckID is the stable identifier for this check.
const PriorityClassExistsCheckID = "priorityclass-exists"

// systemNodeCriticalPriorityClass and systemClusterCriticalPriorityClass are
// the two special keywords documented on corev1.PodSpec.PriorityClassName
// (k8s.io/api/core/v1/types.go): "'system-node-critical' and
// 'system-cluster-critical' are two special keywords which indicate the
// highest priorities with the former being the highest priority. Any other
// name must be defined by creating a PriorityClass object with that name."
// That phrasing exempts exactly these two names from needing a real
// PriorityClass object to resolve: no exported constant for them exists in
// k8s.io/api (they are literal strings even in the vendored doc comment), so
// they are reproduced here rather than invented independently.
const (
	systemNodeCriticalPriorityClass    = "system-node-critical"
	systemClusterCriticalPriorityClass = "system-cluster-critical"
)

// PriorityClassExists checks that a pod template's priorityClassName, when
// set, actually resolves to a PriorityClass object. PriorityClass is
// cluster-scoped (there is no per-namespace variant), so this is a single
// Get by name, the same verifiable-fact shape already used by
// serviceaccount-exists and pvc-exists, not a heuristic.
//
// Unset/empty priorityClassName is not flagged: per the same PodSpec doc
// comment, an unset value simply defaults to the cluster's default priority,
// or zero if there is none — a legitimate, common configuration, not a
// misconfiguration. The two system special keywords above are treated the
// same way: they are guaranteed valid by the API's own contract regardless
// of whether an object with that literal name happens to exist, so this
// check does not Get them at all.
//
// Verified on kind (v1.37.0): the apiserver's Priority admission plugin does
// reject Pod creation outright for a non-empty, non-system
// priorityClassName that does not resolve — a Deployment referencing one
// never creates a single Pod, the same failure shape already covered
// reactively by internal/diagnose's serviceaccount-missing cause:
//
//	Warning  FailedCreate  replicaset/x-xxxxxxxxxx  Error creating: pods
//	"x-xxxxxxxxxx-" is forbidden: no PriorityClass with name
//	does-not-exist-at-all was found
//
// This check exists so `check` catches it before the rollout starts rather
// than only after `watch` observes the FailedCreate event — mirroring why
// serviceaccount-exists was added alongside serviceaccount-missing. No
// watch-side diagnoser for this cause exists yet: it would need the same
// Quota-style event-message matching serviceaccount-missing already does,
// left for a future change rather than folded into this one.
type PriorityClassExists struct{}

// ID implements check.Check.
func (PriorityClassExists) ID() string { return PriorityClassExistsCheckID }

// Run implements check.Check.
func (c PriorityClassExists) Run(ctx context.Context, target Target) (Result, error) {
	name := target.Workload.PriorityClassName()
	if name == "" || name == systemNodeCriticalPriorityClass || name == systemClusterCriticalPriorityClass {
		return Result{CheckID: c.ID()}, nil
	}

	_, err := target.Client.SchedulingV1().PriorityClasses().Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		return Result{CheckID: c.ID()}, nil
	case apierrors.IsNotFound(err):
		workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
		return Result{CheckID: c.ID(), Findings: []model.Finding{{
			CheckID:  c.ID(),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"%s references PriorityClass %q, which does not exist: the pod template's declared priority cannot be applied",
				workloadRef, name,
			),
			Evidence: []string{fmt.Sprintf("priorityClassName=%s", name)},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("create PriorityClass %q, or correct %s's priorityClassName if the name is wrong", name, workloadRef),
				Commands:         []string{fmt.Sprintf("kubectl get priorityclass %s", name)},
				ContextDependent: true,
			},
			Resource: model.ResourceRef{Kind: "PriorityClass", Name: name},
		}}}, nil
	default:
		return Skip(c.ID(), fmt.Sprintf("failed to read PriorityClass %q: %v", name, err)), nil
	}
}
