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
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

func TestQuotaHeadroom_NoQuota(t *testing.T) {
	one := intstr.FromString("25%")
	res := runQuotaCheck(t, deploymentWithRequests(4, &one, "100m", "128Mi"))

	if res.Skipped {
		t.Fatalf("check must not be skipped without ResourceQuota, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings without ResourceQuota in the namespace, got %d: %+v", len(res.Findings), res.Findings)
	}
}

func TestQuotaHeadroom_Recreate_NoSurge(t *testing.T) {
	d := deploymentWithRequests(4, nil, "100m", "128Mi")
	d.Spec.Strategy = recreateStrategy()
	res := runQuotaCheck(t, d,
		resourceQuota("tight", corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")}, corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")}),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("Recreate does not surge; want 0 findings even with a full quota, got %+v", res.Findings)
	}
}

func TestQuotaHeadroom_SufficientHeadroom_NoFindings(t *testing.T) {
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
		t.Fatalf("ample headroom: want 0 findings, got %+v", res.Findings)
	}
}

func TestQuotaHeadroom_InsufficientPods_High(t *testing.T) {
	surge := intstr.FromInt(1)
	res := runQuotaCheck(t, deploymentWithRequests(4, &surge, "100m", "128Mi"),
		resourceQuota("stretta",
			corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")},
			corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")},
		),
	)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for exhausted pods quota, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.CheckID != check.QuotaHeadroomCheckID {
		t.Errorf("checkID = %q, want %q", f.CheckID, check.QuotaHeadroomCheckID)
	}
	if !f.Remediation.ContextDependent {
		t.Errorf("remediation for insufficient quota must declare itself context-dependent")
	}
}

func TestQuotaHeadroom_InsufficientCPU_High(t *testing.T) {
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
		t.Fatalf("want 1 finding for insufficient CPU (\"cpu\" alias), got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

func TestQuotaHeadroom_QuotaWithoutRelevantKeys_Ignored(t *testing.T) {
	surge := intstr.FromInt(1)
	res := runQuotaCheck(t, deploymentWithRequests(4, &surge, "100m", "128Mi"),
		resourceQuota("solo-storage",
			corev1.ResourceList{corev1.ResourceName("requests.storage"): resource.MustParse("1Gi")},
			corev1.ResourceList{corev1.ResourceName("requests.storage"): resource.MustParse("100Mi")},
		),
	)

	if len(res.Findings) != 0 {
		t.Fatalf("a quota that does not constrain pods/cpu/memory must not produce findings, got %+v", res.Findings)
	}
}

// StatefulSet has no MaxSurge field at all: its rolling update deletes a pod
// before creating its replacement, one ordinal at a time, so it never
// exceeds Replicas. surgeCount() must resolve to ok=false purely because
// UpdateStrategy().MaxSurge is nil (no special-casing of workload.Kind is
// expected or desired), producing zero findings even against a ResourceQuota
// with no headroom at all.
func TestQuotaHeadroom_StatefulSet_RollingUpdate_NoSurge(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(4),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "example.com/app:v1",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					}},
				},
			},
		},
	}
	quota := resourceQuota("tight", corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")}, corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")})
	client := fake.NewSimpleClientset(s, quota)

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromStatefulSet(s),
		Client:    client,
	}

	res, err := check.QuotaHeadroom{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("StatefulSet rolling update never surges; want 0 findings even with a full quota, got %+v", res.Findings)
	}
}

func TestQuotaHeadroom_ListFailed_Skipped(t *testing.T) {
	surge := intstr.FromInt(1)
	d := deploymentWithRequests(4, &surge, "100m", "128Mi")
	client := fake.NewSimpleClientset(d)
	client.PrependReactor("list", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading ResourceQuotas")
	})

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}

	res, err := check.QuotaHeadroom{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("want Skipped=true when the ResourceQuota list is not accessible")
	}
	if res.SkipReason == "" {
		t.Errorf("SkipReason must not be empty")
	}
}
