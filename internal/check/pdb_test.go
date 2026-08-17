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
		t.Fatalf("Run() ha restituito un errore inatteso: %v", err)
	}
	return res
}

func TestPDBConsistency_NessunPDB(t *testing.T) {
	res := runPDBCheck(t, deployment(3, rollingUpdateStrategy(nil)))

	if res.Skipped {
		t.Fatalf("il check non deve essere skipped in assenza di PDB, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("attesi 0 finding senza PDB nel namespace, got %d: %+v", len(res.Findings), res.Findings)
	}
}

func TestPDBConsistency_MaxUnavailableZero_High(t *testing.T) {
	zero := intstr.FromInt(0)
	res := runPDBCheck(t,
		deployment(3, rollingUpdateStrategy(nil)),
		pdb("checkout-pdb", podLabels(), nil, intstrPtr(zero)),
	)

	if len(res.Findings) != 1 {
		t.Fatalf("attesi 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, atteso High", f.Severity)
	}
	if f.CheckID != check.PDBCheckID {
		t.Errorf("checkID = %q, atteso %q", f.CheckID, check.PDBCheckID)
	}
	if !f.Remediation.ContextDependent {
		t.Errorf("la remediation su un PDB troppo restrittivo deve dichiararsi context-dependent")
	}
}

func TestPDBConsistency_MinAvailableUgualeAReplicas_High(t *testing.T) {
	three := intstr.FromInt(3)
	res := runPDBCheck(t,
		deployment(3, rollingUpdateStrategy(nil)),
		pdb("checkout-pdb", podLabels(), intstrPtr(three), nil),
	)

	if len(res.Findings) != 1 || res.Findings[0].Severity != model.SeverityHigh {
		t.Fatalf("atteso 1 finding High per minAvailable >= replicas, got %+v", res.Findings)
	}
}

func TestPDBConsistency_BudgetSufficiente_NessunFinding(t *testing.T) {
	one := intstr.FromInt(1)
	res := runPDBCheck(t,
		deployment(3, rollingUpdateStrategy(nil)),
		pdb("checkout-pdb", podLabels(), nil, intstrPtr(one)),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("attesi 0 finding con maxUnavailable=1 su 3 repliche, got %+v", res.Findings)
	}
}

func TestPDBConsistency_SelectorNonCorrispondente_Ignorato(t *testing.T) {
	zero := intstr.FromInt(0)
	res := runPDBCheck(t,
		deployment(3, rollingUpdateStrategy(nil)),
		pdb("altro-pdb", map[string]string{"app": "altro-servizio"}, nil, intstrPtr(zero)),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("un PDB con selector non corrispondente non deve produrre finding, got %+v", res.Findings)
	}
}

func TestPDBConsistency_RecreateConBudgetParziale_High(t *testing.T) {
	one := intstr.FromInt(1)
	res := runPDBCheck(t,
		deployment(3, recreateStrategy()),
		pdb("checkout-pdb", podLabels(), nil, intstrPtr(one)),
	)

	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding per Recreate + PDB parziale, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, atteso High", res.Findings[0].Severity)
	}
}

func TestPDBConsistency_RecreateConBudgetPieno_NessunFinding(t *testing.T) {
	three := intstr.FromInt(3)
	res := runPDBCheck(t,
		deployment(3, recreateStrategy()),
		pdb("checkout-pdb", podLabels(), nil, intstrPtr(three)),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("un PDB che permette la disruption di tutte le repliche non deve confliggere con Recreate, got %+v", res.Findings)
	}
}

func TestPDBConsistency_ListaFallita_Skipped(t *testing.T) {
	client := fake.NewSimpleClientset(deployment(3, rollingUpdateStrategy(nil)))
	client.PrependReactor("list", "poddisruptionbudgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC nega la lettura dei PodDisruptionBudget")
	})

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(deployment(3, rollingUpdateStrategy(nil))),
		Client:    client,
	}

	res, err := check.PDBConsistency{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() non deve restituire errore su lista fallita, deve degradare a Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("atteso Skipped=true quando la lista dei PDB non e' accessibile")
	}
	if res.SkipReason == "" {
		t.Errorf("SkipReason non deve essere vuoto")
	}
}
