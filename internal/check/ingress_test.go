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

func serviceFrontingWorkload(name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       corev1.ServiceSpec{Selector: podLabels(), Ports: ports},
	}
}

func pathType() *netv1.PathType {
	p := netv1.PathTypePrefix
	return &p
}

func ingressWithBackend(name, svcName string, port netv1.ServiceBackendPort) *netv1.Ingress {
	return &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{{
				IngressRuleValue: netv1.IngressRuleValue{
					HTTP: &netv1.HTTPIngressRuleValue{
						Paths: []netv1.HTTPIngressPath{{
							PathType: pathType(),
							Path:     "/",
							Backend: netv1.IngressBackend{
								Service: &netv1.IngressServiceBackend{Name: svcName, Port: port},
							},
						}},
					},
				},
			}},
		},
	}
}

func runIngressRoutingCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.IngressRouting{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestIngressRouting_PortNumberResolved_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	ing := ingressWithBackend("checkout", "checkout", netv1.ServiceBackendPort{Number: 80})

	result := runIngressRoutingCheck(t, d, svc, ing)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("Ingress backend port resolves against the Service: want empty result, got %+v", result)
	}
}

func TestIngressRouting_PortNumberUnresolved_HighFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	ing := ingressWithBackend("checkout", "checkout", netv1.ServiceBackendPort{Number: 8080})

	result := runIngressRoutingCheck(t, d, svc, ing)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the unresolved backend port, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.IngressRoutingCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "checkout") || !strings.Contains(f.Cause, "8080") {
		t.Errorf("cause must name the Ingress/Service and the unresolved port, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
	if f.Resource.Kind != "Ingress" || f.Resource.Name != "checkout" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

func TestIngressRouting_PortNameUnresolved_HighFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080)})
	ing := ingressWithBackend("checkout", "checkout", netv1.ServiceBackendPort{Name: "https"})

	result := runIngressRoutingCheck(t, d, svc, ing)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the unresolved named backend port, got %+v", result)
	}
	if !strings.Contains(result.Findings[0].Cause, "https") {
		t.Errorf("cause must name the unresolved port name, got %q", result.Findings[0].Cause)
	}
}

func TestIngressRouting_ServiceNotFrontingWorkload_Ignored(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	// The Ingress references a Service that has nothing to do with this
	// workload: whether that reference is broken is not this check's
	// concern when the target is a different workload entirely.
	ing := ingressWithBackend("other", "other-service", netv1.ServiceBackendPort{Number: 80})

	result := runIngressRoutingCheck(t, d, ing)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("Ingress does not target this workload's Service: want empty result, got %+v", result)
	}
}

func TestIngressRouting_NoServiceFrontsWorkload_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})

	result := runIngressRoutingCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no Service fronts this workload: service-routing's concern, not this one's, got %+v", result)
	}
}

func TestIngressRouting_DefaultBackend_Checked(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	ing := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: netv1.IngressSpec{
			DefaultBackend: &netv1.IngressBackend{
				Service: &netv1.IngressServiceBackend{Name: "checkout", Port: netv1.ServiceBackendPort{Number: 9999}},
			},
		},
	}

	result := runIngressRoutingCheck(t, d, svc, ing)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("defaultBackend must be checked the same as rule backends, got %+v", result)
	}
}

func TestIngressRouting_ServiceListFailed_Skipped(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "services", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing Services")
	})

	result, err := check.IngressRouting{}.Run(context.Background(), check.Target{
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

func TestIngressRouting_IngressListFailed_Skipped(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := serviceFrontingWorkload("checkout", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)})
	client := fake.NewSimpleClientset(svc)
	client.PrependReactor("list", "ingresses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing Ingresses")
	})

	result, err := check.IngressRouting{}.Run(context.Background(), check.Target{
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
