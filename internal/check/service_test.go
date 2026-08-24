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
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func deploymentWithSelectorContainers(containers ...corev1.Container) *appsv1.Deployment {
	d := deploymentWithContainers(containers...)
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: podLabels()}
	return d
}

func readyPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: podLabels()},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func boolPtr(v bool) *bool { return &v }

func readyEndpointSlice(serviceName string, n int) *discoveryv1.EndpointSlice {
	endpoints := make([]discoveryv1.Endpoint, n)
	for i := range endpoints {
		endpoints[i] = discoveryv1.Endpoint{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)},
		}
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: testNamespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: serviceName},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   endpoints,
	}
}

func runServiceRoutingCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.ServiceRouting{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestServiceRouting_NoMatchingService_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	other := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: testNamespace},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "cart"}},
	}

	result := runServiceRoutingCheck(t, d, other)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no Service selects this workload: want empty result, got %+v", result)
	}
}

func TestServiceRouting_HeadlessSelectorlessService_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "headless", Namespace: testNamespace},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
	}

	result := runServiceRoutingCheck(t, d, svc)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("a Service with no label selector is not this check's concern: got %+v", result)
	}
}

func TestServiceRouting_NamedTargetPortMissing_HighFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Selector: podLabels(),
			Ports:    []corev1.ServicePort{{Name: "http", TargetPort: intstr.FromString("http")}},
		},
	}

	result := runServiceRoutingCheck(t, d, svc)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the unresolved named port, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.ServiceRoutingCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High: an unresolved named targetPort is a verified, guaranteed break", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "checkout") || !strings.Contains(f.Cause, "http") {
		t.Errorf("cause must name the Service and the unresolved port, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent: fixing the container port vs. the Service's targetPort are different actions this tool cannot choose between")
	}
	if f.Resource.Kind != "Service" || f.Resource.Name != "checkout" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

func TestServiceRouting_NamedTargetPortResolved_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{
		Name:  "app",
		Image: "nginx:1.27",
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
	})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Selector: podLabels(),
			Ports:    []corev1.ServicePort{{Name: "http", TargetPort: intstr.FromString("http")}},
		},
	}
	pod := readyPod("checkout-abc")
	ep := readyEndpointSlice("checkout", 1)

	result := runServiceRoutingCheck(t, d, svc, pod, ep)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("named port resolves and endpoints are ready: want empty result, got %+v", result)
	}
}

func TestServiceRouting_ReadyPodsZeroEndpoints_HighFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{
		Name:  "app",
		Image: "nginx:1.27",
		Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
	})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Selector: podLabels(),
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(8080)}},
		},
	}
	pod := readyPod("checkout-abc")
	// No Endpoints object at all: the endpoint controller has not
	// populated one, which must be treated the same as zero addresses.

	result := runServiceRoutingCheck(t, d, svc, pod)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for zero endpoints despite a ready pod, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.ServiceRoutingCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High: traffic is guaranteed dropped", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "checkout") || !strings.Contains(f.Cause, "1 ready pod") {
		t.Errorf("cause must name the Service and the observed ready pod count, got %q", f.Cause)
	}
	if len(f.Remediation.Commands) != 1 || !strings.Contains(f.Remediation.Commands[0], "checkout") {
		t.Errorf("remediation command must be a read-only inspection of the Service's Endpoints, got %+v", f.Remediation.Commands)
	}
}

func TestServiceRouting_NoReadyPodsYet_NoEndpointsFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{
		Name:  "app",
		Image: "nginx:1.27",
		Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
	})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Selector: podLabels(),
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(8080)}},
		},
	}
	notReady := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-abc", Namespace: testNamespace, Labels: podLabels()},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	}

	result := runServiceRoutingCheck(t, d, svc, notReady)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no pod is Ready yet: the Service having zero endpoints is expected, not this check's finding to give, got %+v", result)
	}
}

// Discovered by reasoning about a single-replica rolling update: the
// endpoint controller drops a terminating pod from EndpointSlice
// immediately, but the kubelet does not necessarily flip the pod's own
// PodReady condition to false at that same instant. Without excluding
// terminating pods, the brief window where the old pod is gone from
// endpoints but still reports Ready=true would read as a false positive.
func TestServiceRouting_TerminatingReadyPod_NoEndpointsFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{
		Name:  "app",
		Image: "nginx:1.27",
		Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
	})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Selector: podLabels(),
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(8080)}},
		},
	}
	now := metav1.Now()
	terminating := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "checkout-old",
			Namespace:         testNamespace,
			Labels:            podLabels(),
			DeletionTimestamp: &now,
			Finalizers:        []string{"kubernetes"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	// No EndpointSlice object: the endpoint controller has already
	// dropped the terminating pod, and no replacement is ready yet.

	result := runServiceRoutingCheck(t, d, svc, terminating)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("a terminating pod must not count as a ready pod for the endpoints comparison, got %+v", result)
	}
}

func TestServiceRouting_ServiceListFailed_Skipped(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "services", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing Services")
	})

	result, err := check.ServiceRouting{}.Run(context.Background(), check.Target{
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
