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

// ServiceAccountExistsCheckID is the stable identifier for this check.
const ServiceAccountExistsCheckID = "serviceaccount-exists"

// ServiceAccountExists checks that the ServiceAccount named in the pod
// template actually exists. Unlike image-pull-secrets, there is no
// hostname-style heuristic involved: whether the named ServiceAccount
// resolves is a plain fact, checked with one Get call, so a missing one is
// Severity High rather than a guess. Found worth adding after
// internal/diagnose's serviceaccount-missing cause: a Deployment naming a
// ServiceAccount the API server cannot find will never create a single
// Pod (the ReplicaSet controller is rejected at admission, observed on
// kind as a FailedCreate event), so this is exactly the kind of failure
// `check` exists to catch before the rollout ever starts, not only report
// reactively once `watch` sees it happen.
type ServiceAccountExists struct{}

// ID implements check.Check.
func (ServiceAccountExists) ID() string { return ServiceAccountExistsCheckID }

// Run implements check.Check.
func (c ServiceAccountExists) Run(ctx context.Context, target Target) (Result, error) {
	name := target.Workload.ServiceAccountName()
	if name == "" {
		// Kubernetes' implicit "default": every namespace has one once the
		// ServiceAccount admission controller has run. Checking it
		// explicitly still matters, since a namespace can briefly lack it
		// right after creation.
		name = "default"
	}

	_, err := target.Client.CoreV1().ServiceAccounts(target.Namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		return Result{CheckID: c.ID()}, nil
	case apierrors.IsNotFound(err):
		workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
		return Result{CheckID: c.ID(), Findings: []model.Finding{{
			CheckID:  c.ID(),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"%s references ServiceAccount %q, which does not exist in namespace %q: the ReplicaSet controller will be unable to create any Pod",
				workloadRef, name, target.Namespace,
			),
			Evidence: []string{fmt.Sprintf("serviceAccount=%s", name)},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("create ServiceAccount %q in namespace %q, or correct %s's serviceAccountName if the name is wrong", name, target.Namespace, workloadRef),
				Commands:         []string{fmt.Sprintf("kubectl get serviceaccount %s -n %s", name, target.Namespace)},
				ContextDependent: true,
			},
			Resource: model.ResourceRef{Kind: "ServiceAccount", Namespace: target.Namespace, Name: name},
		}}}, nil
	default:
		return Skip(c.ID(), fmt.Sprintf("failed to read ServiceAccount %q: %v", name, err)), nil
	}
}
