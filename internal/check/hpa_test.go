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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func hpaTargeting(name, workloadKind, workloadName string, minReplicas, maxReplicas int32) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: workloadKind, Name: workloadName},
			MinReplicas:    &minReplicas,
			MaxReplicas:    maxReplicas,
		},
	}
}

func runHPAQuotaCheck(t *testing.T, d *appsv1.Deployment, objs ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objs...)
	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}
	res, err := check.HPAQuotaHeadroom{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

func TestHPAQuotaHeadroom_NoHPA_NoFindings(t *testing.T) {
	d := deploymentWithRequests(3, nil, "100m", "128Mi")
	q := resourceQuota("compute", corev1.ResourceList{corev1.ResourcePods: resource.MustParse("4")}, corev1.ResourceList{corev1.ResourcePods: resource.MustParse("3")})

	res := runHPAQuotaCheck(t, d, q)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("no HPA targets this workload: want empty result, got %+v", res)
	}
}

func TestHPAQuotaHeadroom_HPATargetsDifferentWorkload_Ignored(t *testing.T) {
	d := deploymentWithRequests(3, nil, "100m", "128Mi")
	hpa := hpaTargeting("other-hpa", "Deployment", "other-workload", 1, 10)

	res := runHPAQuotaCheck(t, d, hpa)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("HPA targets a different workload: want empty result, got %+v", res)
	}
}

func TestHPAQuotaHeadroom_MaxReplicasBelowCurrentDesired_NoFindings(t *testing.T) {
	d := deploymentWithRequests(5, nil, "100m", "128Mi")
	// Manually scaled above the HPA's own ceiling: unusual, but the HPA
	// cannot ask for more than it already has, so there is nothing extra
	// to check headroom for.
	hpa := hpaTargeting("checkout-hpa", "Deployment", "checkout", 1, 3)

	res := runHPAQuotaCheck(t, d, hpa)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("maxReplicas <= current desired replicas: want empty result, got %+v", res)
	}
}

func TestHPAQuotaHeadroom_InsufficientPodQuota_MediumFinding(t *testing.T) {
	d := deploymentWithRequests(3, nil, "100m", "128Mi")
	hpa := hpaTargeting("checkout-hpa", "Deployment", "checkout", 1, 10)
	// maxReplicas=10, current=3: 7 additional pods needed. Quota allows
	// only 5 total, 3 already used: headroom for 2, not 7.
	q := resourceQuota("compute", corev1.ResourceList{corev1.ResourcePods: resource.MustParse("5")}, corev1.ResourceList{corev1.ResourcePods: resource.MustParse("3")})

	res := runHPAQuotaCheck(t, d, hpa, q)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for insufficient pod-count headroom, got %+v", res)
	}
	f := res.Findings[0]
	if f.CheckID != check.HPAQuotaHeadroomCheckID || f.Severity != model.SeverityMedium {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want Medium: not a guaranteed blockage of the current rollout, only an eroded margin", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "checkout-hpa") || !strings.Contains(f.Cause, "10") {
		t.Errorf("cause must name the HPA and its maxReplicas, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
}

func TestHPAQuotaHeadroom_SufficientQuota_NoFindings(t *testing.T) {
	d := deploymentWithRequests(3, nil, "100m", "128Mi")
	hpa := hpaTargeting("checkout-hpa", "Deployment", "checkout", 1, 5)
	// maxReplicas=5, current=3: 2 additional pods needed, quota allows it.
	q := resourceQuota("compute", corev1.ResourceList{
		corev1.ResourcePods:                    resource.MustParse("10"),
		corev1.ResourceName("requests.cpu"):    resource.MustParse("2"),
		corev1.ResourceName("requests.memory"): resource.MustParse("2Gi"),
	}, corev1.ResourceList{
		corev1.ResourcePods:                    resource.MustParse("3"),
		corev1.ResourceName("requests.cpu"):    resource.MustParse("300m"),
		corev1.ResourceName("requests.memory"): resource.MustParse("384Mi"),
	})

	res := runHPAQuotaCheck(t, d, hpa, q)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("quota has enough headroom for the HPA's maxReplicas: want empty result, got %+v", res)
	}
}

func TestHPAQuotaHeadroom_NoResourceQuota_NoFindings(t *testing.T) {
	d := deploymentWithRequests(3, nil, "100m", "128Mi")
	hpa := hpaTargeting("checkout-hpa", "Deployment", "checkout", 1, 10)

	res := runHPAQuotaCheck(t, d, hpa)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("no ResourceQuota in the namespace: want empty result, got %+v", res)
	}
}

func TestHPAQuotaHeadroom_HPAListFailed_Skipped(t *testing.T) {
	d := deploymentWithRequests(3, nil, "100m", "128Mi")
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "horizontalpodautoscalers", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing HorizontalPodAutoscalers")
	})

	target := check.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d), Client: client}
	res, err := check.HPAQuotaHeadroom{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatal("want Skipped=true when the HorizontalPodAutoscaler list is not accessible")
	}
	if res.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
