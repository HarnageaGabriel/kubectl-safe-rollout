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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func deploymentWithRequests(replicas int32, maxSurge *intstr.IntOrString, cpu, memory string) *appsv1.Deployment {
	d := deployment(replicas, rollingUpdateStrategyWithSurge(maxSurge))
	d.Spec.Template.Spec.Containers = []corev1.Container{{
		Name:  "app",
		Image: "example.com/app:v1",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
			},
		},
	}}
	return d
}

func rollingUpdateStrategyWithSurge(maxSurge *intstr.IntOrString) appsv1.DeploymentStrategy {
	return appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxSurge: maxSurge,
		},
	}
}

func resourceQuota(name string, hard corev1.ResourceList, used corev1.ResourceList) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Status: corev1.ResourceQuotaStatus{
			Hard: hard,
			Used: used,
		},
	}
}

func runQuotaCheck(t *testing.T, d *appsv1.Deployment, objs ...runtime.Object) check.Result {
	t.Helper()
	all := append([]runtime.Object{d}, objs...)
	client := fake.NewSimpleClientset(all...)

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}

	res, err := check.QuotaHeadroom{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() ha restituito un errore inatteso: %v", err)
	}
	return res
}

func TestQuotaHeadroom_NessunaQuota(t *testing.T) {
	one := intstr.FromString("25%")
	res := runQuotaCheck(t, deploymentWithRequests(4, &one, "100m", "128Mi"))

	if res.Skipped {
		t.Fatalf("il check non deve essere skipped senza ResourceQuota, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("attesi 0 finding senza ResourceQuota nel namespace, got %d: %+v", len(res.Findings), res.Findings)
	}
}

func TestQuotaHeadroom_Recreate_NessunSurge(t *testing.T) {
	d := deploymentWithRequests(4, nil, "100m", "128Mi")
	d.Spec.Strategy = recreateStrategy()
	res := runQuotaCheck(t, d,
		resourceQuota("tight", corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")}, corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")}),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("Recreate non fa surge, attesi 0 finding anche con quota piena, got %+v", res.Findings)
	}
}

func TestQuotaHeadroom_MargineSufficiente_NessunFinding(t *testing.T) {
	surge := intstr.FromInt(1)
	res := runQuotaCheck(t, deploymentWithRequests(4, &surge, "100m", "128Mi"),
		resourceQuota("ampia",
			corev1.ResourceList{
				corev1.ResourcePods:                    resource.MustParse("10"),
				corev1.ResourceName("requests.cpu"):    resource.MustParse("2"),
				corev1.ResourceName("requests.memory"): resource.MustParse("2Gi"),
			},
			corev1.ResourceList{
				corev1.ResourcePods:                    resource.MustParse("4"),
				corev1.ResourceName("requests.cpu"):    resource.MustParse("400m"),
				corev1.ResourceName("requests.memory"): resource.MustParse("512Mi"),
			},
		),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("margine ampio, attesi 0 finding, got %+v", res.Findings)
	}
}

func TestQuotaHeadroom_PodsInsufficienti_High(t *testing.T) {
	surge := intstr.FromInt(1)
	res := runQuotaCheck(t, deploymentWithRequests(4, &surge, "100m", "128Mi"),
		resourceQuota("stretta",
			corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")},
			corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")},
		),
	)

	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding per quota pods esaurita, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, atteso High", f.Severity)
	}
	if f.CheckID != check.QuotaHeadroomCheckID {
		t.Errorf("checkID = %q, atteso %q", f.CheckID, check.QuotaHeadroomCheckID)
	}
	if !f.Remediation.ContextDependent {
		t.Errorf("la remediation su una quota insufficiente deve dichiararsi context-dependent")
	}
}

func TestQuotaHeadroom_CPUInsufficiente_High(t *testing.T) {
	surge := intstr.FromInt(2)
	res := runQuotaCheck(t, deploymentWithRequests(4, &surge, "500m", "128Mi"),
		resourceQuota("cpu-stretta",
			corev1.ResourceList{
				corev1.ResourcePods:        resource.MustParse("10"),
				corev1.ResourceName("cpu"): resource.MustParse("2"),
			},
			corev1.ResourceList{
				corev1.ResourcePods:        resource.MustParse("4"),
				corev1.ResourceName("cpu"): resource.MustParse("1900m"),
			},
		),
	)

	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding per cpu insufficiente (alias \"cpu\"), got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, atteso High", res.Findings[0].Severity)
	}
}

func TestQuotaHeadroom_QuotaSenzaChiaviRilevanti_Ignorata(t *testing.T) {
	surge := intstr.FromInt(1)
	res := runQuotaCheck(t, deploymentWithRequests(4, &surge, "100m", "128Mi"),
		resourceQuota("solo-storage",
			corev1.ResourceList{corev1.ResourceName("requests.storage"): resource.MustParse("1Gi")},
			corev1.ResourceList{corev1.ResourceName("requests.storage"): resource.MustParse("100Mi")},
		),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("una quota che non vincola pods/cpu/memory non deve produrre finding, got %+v", res.Findings)
	}
}

func TestQuotaHeadroom_ListaFallita_Skipped(t *testing.T) {
	surge := intstr.FromInt(1)
	d := deploymentWithRequests(4, &surge, "100m", "128Mi")
	client := fake.NewSimpleClientset(d)
	client.PrependReactor("list", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC nega la lettura delle ResourceQuota")
	})

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}

	res, err := check.QuotaHeadroom{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() non deve restituire errore su lista fallita, deve degradare a Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("atteso Skipped=true quando la lista delle ResourceQuota non e' accessibile")
	}
	if res.SkipReason == "" {
		t.Errorf("SkipReason non deve essere vuoto")
	}
}
