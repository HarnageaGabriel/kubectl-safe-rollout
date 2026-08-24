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

package check_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func runServiceAccountExistsCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.ServiceAccountExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestServiceAccountExists_ExplicitAndExisting_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.ServiceAccountName = "deployer"
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "deployer", Namespace: testNamespace}}

	result := runServiceAccountExistsCheck(t, d, sa)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("explicit, existing ServiceAccount: want empty result, got %+v", result)
	}
}

// An empty ServiceAccountName means Kubernetes' implicit "default", which
// this check must still resolve rather than treating an unset field as
// "nothing to verify".
func TestServiceAccountExists_ImplicitDefault_ExistingResolved(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}

	result := runServiceAccountExistsCheck(t, d, sa)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("implicit default ServiceAccount exists: want empty result, got %+v", result)
	}
}

// Discovered on kind: a Deployment referencing a ServiceAccount that does
// not exist never creates a single Pod (rejected at admission on the
// ReplicaSet), so this is High, not a guess.
func TestServiceAccountExists_Missing_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.ServiceAccountName = "this-sa-does-not-exist"

	result := runServiceAccountExistsCheck(t, d)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.ServiceAccountExistsCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High: a missing ServiceAccount deterministically blocks pod creation", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "this-sa-does-not-exist") {
		t.Errorf("cause must name the missing ServiceAccount, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent: creating the ServiceAccount vs. fixing a typo are different actions this tool cannot choose between")
	}
	if len(f.Remediation.Commands) != 1 || !strings.Contains(f.Remediation.Commands[0], "this-sa-does-not-exist") {
		t.Errorf("remediation command must be a read-only inspection of the named ServiceAccount, got %+v", f.Remediation.Commands)
	}
	if f.Resource.Kind != "ServiceAccount" || f.Resource.Name != "this-sa-does-not-exist" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

func TestServiceAccountExists_ImplicitDefault_Missing_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})

	result := runServiceAccountExistsCheck(t, d)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	if !strings.Contains(result.Findings[0].Cause, "default") {
		t.Errorf("cause must name \"default\" as the missing ServiceAccount, got %q", result.Findings[0].Cause)
	}
}

func TestServiceAccountExists_ReadFailed_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading the ServiceAccount")
	})

	result, err := check.ServiceAccountExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the ServiceAccount is not accessible: an RBAC gap must not be reported as a missing ServiceAccount")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
