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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8stesting "k8s.io/client-go/testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

const testNamespace = "default"

func int32Ptr(v int32) *int32 { return &v }

func podLabels() map[string]string {
	return map[string]string{"app": "checkout"}
}

func deployment(replicas int32, strategy appsv1.DeploymentStrategy) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(replicas),
			Strategy: strategy,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels()},
			},
		},
	}
}

func rollingUpdateStrategy(maxUnavailable *intstr.IntOrString) appsv1.DeploymentStrategy {
	return appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: maxUnavailable,
		},
	}
}

func recreateStrategy() appsv1.DeploymentStrategy {
	return appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
}

func pdb(name string, selectorLabels map[string]string, minAvailable, maxUnavailable *intstr.IntOrString) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector:       &metav1.LabelSelector{MatchLabels: selectorLabels},
			MinAvailable:   minAvailable,
			MaxUnavailable: maxUnavailable,
		},
	}
}

func intstrPtr(v intstr.IntOrString) *intstr.IntOrString { return &v }

func runPDBCheck(t *testing.T, d *appsv1.Deployment, objs ...runtime.Object) check.Result {
	t.Helper()
	all := append([]runtime.Object{d}, objs...)
	client := fake.NewSimpleClientset(all...)

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}

	res, err := check.PDBConsistency{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

func TestPDBConsistency_NoPDB(t *testing.T) {
	res := runPDBCheck(t, deployment(3, rollingUpdateStrategy(nil)))

	if res.Skipped {
		t.Fatalf("check must not be skipped when no PDB exists, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings without a PDB in the namespace, got %d: %+v", len(res.Findings), res.Findings)
	}
}

func TestPDBConsistency_MaxUnavailableZero_High(t *testing.T) {
	zero := intstr.FromInt(0)
	res := runPDBCheck(t,
		deployment(3, rollingUpdateStrategy(nil)),
		pdb("checkout-pdb", podLabels(), nil, intstrPtr(zero)),
	)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.CheckID != check.PDBCheckID {
		t.Errorf("checkID = %q, want %q", f.CheckID, check.PDBCheckID)
	}
	if !f.Remediation.ContextDependent {
		t.Errorf("remediation for an overly restrictive PDB must declare itself context-dependent")
	}
}

// Found on kind: a Deployment scaled to zero with a minAvailable PDB
// produced a HIGH finding claiming a concurrent drain "will remain
// blocked" — false, since zero Pods means the Eviction API has nothing to
// act on — and a remediation suggesting "minAvailable: -1", a value the
// PDB admission plugin rejects outright.
func TestPDBConsistency_ZeroReplicas_NoFindings(t *testing.T) {
	fifty := intstr.FromString("50%")
	res := runPDBCheck(t,
		deployment(0, rollingUpdateStrategy(nil)),
		pdb("checkout-pdb", podLabels(), intstrPtr(fifty), nil),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("a workload with zero replicas has nothing for a PDB to protect: want 0 findings, got %+v", res.Findings)
	}
}

func TestPDBConsistency_MinAvailableEqualsReplicas_High(t *testing.T) {
	three := intstr.FromInt(3)
	res := runPDBCheck(t,
		deployment(3, rollingUpdateStrategy(nil)),
		pdb("checkout-pdb", podLabels(), intstrPtr(three), nil),
	)

	if len(res.Findings) != 1 || res.Findings[0].Severity != model.SeverityHigh {
		t.Fatalf("want 1 High finding for minAvailable >= replicas, got %+v", res.Findings)
	}
}

func TestPDBConsistency_SufficientBudget_NoFindings(t *testing.T) {
	one := intstr.FromInt(1)
	res := runPDBCheck(t,
		deployment(3, rollingUpdateStrategy(nil)),
		pdb("checkout-pdb", podLabels(), nil, intstrPtr(one)),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings with maxUnavailable=1 out of 3 replicas, got %+v", res.Findings)
	}
}

func TestPDBConsistency_NonMatchingSelector_Ignored(t *testing.T) {
	zero := intstr.FromInt(0)
	res := runPDBCheck(t,
		deployment(3, rollingUpdateStrategy(nil)),
		pdb("altro-pdb", map[string]string{"app": "altro-servizio"}, nil, intstrPtr(zero)),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("a PDB with a non-matching selector must not produce findings, got %+v", res.Findings)
	}
}

func TestPDBConsistency_RecreateWithPartialBudget_High(t *testing.T) {
	one := intstr.FromInt(1)
	res := runPDBCheck(t,
		deployment(3, recreateStrategy()),
		pdb("checkout-pdb", podLabels(), nil, intstrPtr(one)),
	)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for Recreate plus partial PDB, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

func TestPDBConsistency_RecreateWithFullBudget_NoFindings(t *testing.T) {
	three := intstr.FromInt(3)
	res := runPDBCheck(t,
		deployment(3, recreateStrategy()),
		pdb("checkout-pdb", podLabels(), nil, intstrPtr(three)),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("a PDB allowing disruption of all replicas must not conflict with Recreate, got %+v", res.Findings)
	}
}

func TestPDBConsistency_ListFailed_Skipped(t *testing.T) {
	client := fake.NewSimpleClientset(deployment(3, rollingUpdateStrategy(nil)))
	client.PrependReactor("list", "poddisruptionbudgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading PodDisruptionBudgets")
	})

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(deployment(3, rollingUpdateStrategy(nil))),
		Client:    client,
	}

	res, err := check.PDBConsistency{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("want Skipped=true when the PDB list is not accessible")
	}
	if res.SkipReason == "" {
		t.Errorf("SkipReason must not be empty")
	}
}
