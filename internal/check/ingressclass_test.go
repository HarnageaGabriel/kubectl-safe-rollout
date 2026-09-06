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
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func ingressWithClass(name, svcName, className string, port netv1.ServiceBackendPort) *netv1.Ingress {
	ing := ingressWithBackend(name, svcName, port)
	if className != "" {
		ing.Spec.IngressClassName = &className
	}
	return ing
}

func runIngressClassExistsCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.IngressClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestIngressClassExists_NoServiceFrontsWorkload_NoFindingsNoAPICalls(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()

	var ingressListed, ingressClassGot bool
	client.PrependReactor("list", "ingresses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ingressListed = true
		return false, nil, nil
	})
	client.PrependReactor("get", "ingressclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ingressClassGot = true
		return false, nil, nil
	})

	result, err := check.IngressClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no Service fronts this workload: want empty result, got %+v", result)
	}
	if ingressListed {
		t.Error("Ingress list must not be attempted when no Service fronts the workload")
	}
	if ingressClassGot {
		t.Error("IngressClass get must not be attempted when no Service fronts the workload")
	}
}

func TestIngressClassExists_NoIngressClassNameSet_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	ing := ingressWithBackend("checkout", "checkout", netv1.ServiceBackendPort{Number: 80})

	result := runIngressClassExistsCheck(t, d, svc, ing)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("ingressClassName unset is a legitimate configuration: want empty result, got %+v", result)
	}
}

func TestIngressClassExists_IngressClassExists_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	ing := ingressWithClass("checkout", "checkout", "nginx", netv1.ServiceBackendPort{Number: 80})
	class := &netv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}}

	result := runIngressClassExistsCheck(t, d, svc, ing, class)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("IngressClass exists: want empty result, got %+v", result)
	}
}

func TestIngressClassExists_IngressClassMissing_HighFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	ing := ingressWithClass("checkout", "checkout", "does-not-exist", netv1.ServiceBackendPort{Number: 80})

	result := runIngressClassExistsCheck(t, d, svc, ing)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the missing IngressClass, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.IngressClassExistsCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "checkout") || !strings.Contains(f.Cause, "does-not-exist") {
		t.Errorf("cause must name the Ingress and the missing IngressClass, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
	if f.Resource.Kind != "Ingress" || f.Resource.Name != "checkout" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

func TestIngressClassExists_UnrelatedIngress_Ignored(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	// This Ingress routes to a Service that has nothing to do with this
	// workload: its ingressClassName is broken, but it is out of scope,
	// proving the relevance filter is actually applied.
	ing := ingressWithClass("other", "other-service", "does-not-exist", netv1.ServiceBackendPort{Number: 80})

	result := runIngressClassExistsCheck(t, d, ing)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("Ingress does not front this workload: want empty result, got %+v", result)
	}
}

func TestIngressClassExists_DefaultBackendRelevant_HighFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	className := "does-not-exist"
	ing := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: netv1.IngressSpec{
			IngressClassName: &className,
			DefaultBackend: &netv1.IngressBackend{
				Service: &netv1.IngressServiceBackend{Name: "checkout", Port: netv1.ServiceBackendPort{Number: 80}},
			},
		},
	}

	result := runIngressClassExistsCheck(t, d, svc, ing)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("defaultBackend-relevant Ingress must be checked the same as a rule-backed one, got %+v", result)
	}
	if !strings.Contains(result.Findings[0].Cause, "checkout") {
		t.Errorf("cause must name the Ingress, got %q", result.Findings[0].Cause)
	}
}

func TestIngressClassExists_ServiceListFailed_Skipped(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "services", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing Services")
	})

	result, err := check.IngressClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the Service list is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

func TestIngressClassExists_IngressListFailed_Skipped(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	client := fake.NewSimpleClientset(svc)
	client.PrependReactor("list", "ingresses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing Ingresses")
	})

	result, err := check.IngressClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the Ingress list is not accessible")
	}
}

func TestIngressClassExists_IngressClassGetForbidden_Skipped(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	ing := ingressWithClass("checkout", "checkout", "nginx", netv1.ServiceBackendPort{Number: 80})
	client := fake.NewSimpleClientset(svc, ing)
	client.PrependReactor("get", "ingressclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies getting IngressClasses")
	})

	result, err := check.IngressClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the IngressClass get is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

func TestIngressClassExists_MultipleIngresses_OnlyBadOneReported(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	good := ingressWithClass("checkout-good", "checkout", "nginx", netv1.ServiceBackendPort{Number: 80})
	bad := ingressWithClass("checkout-bad", "checkout", "does-not-exist", netv1.ServiceBackendPort{Number: 80})
	class := &netv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}}

	result := runIngressClassExistsCheck(t, d, svc, good, bad, class)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want exactly one finding for the bad Ingress, got %+v", result)
	}
	if result.Findings[0].Resource.Name != "checkout-bad" {
		t.Errorf("finding must name the Ingress with the bad ingressClassName, got %+v", result.Findings[0].Resource)
	}
}
