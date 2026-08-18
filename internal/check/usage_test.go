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
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1api "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func podWithRequests(name, cpu, memory string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: podLabels()},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(cpu),
						corev1.ResourceMemory: resource.MustParse(memory),
					},
				},
			}},
		},
	}
}

func podMetrics(podName, cpuUsage, memUsage string) metricsv1beta1api.PodMetrics {
	return metricsv1beta1api.PodMetrics{
		// Labels mirror those of the source pod: a real metrics-server
		// copies them to PodMetrics, which allows the List with a
		// LabelSelector (used by RequestsVsUsage to restrict results to
		// workload pods) to find it. Without them, the generated fake
		// clientset (gentype.FakeClientWithList) filters reactor-returned
		// objects client-side by selector and would discard an object
		// without Labels regardless of the reactor.
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: testNamespace, Labels: podLabels()},
		Containers: []metricsv1beta1api.ContainerMetrics{{
			Name: "app",
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpuUsage),
				corev1.ResourceMemory: resource.MustParse(memUsage),
			},
		}},
	}
}

// metricsClientWith builds a fake metrics clientset that responds to the
// PodMetrics List with the given items. PodMetrics cannot be seeded by
// passing them to metricsfake.NewSimpleClientset: the generic ObjectTracker
// derives the resource from Kind using naive pluralization ("PodMetrics" ->
// something like "podmetricses"), while the generated typed client actually
// uses the "pods" resource in the metrics.k8s.io group (see
// k8s.io/metrics/.../typed/metrics/v1beta1/fake/fake_podmetrics.go,
// WithResource("pods")): the two never meet, so the List would always be
// empty. A reactor that responds directly on the "pods" resource bypasses
// the problem.
func metricsClientWith(items ...metricsv1beta1api.PodMetrics) *metricsfake.Clientset {
	c := metricsfake.NewSimpleClientset()
	c.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1api.PodMetricsList{Items: items}, nil
	})
	return c
}

func deploymentWithSelector(replicas int32) *appsv1.Deployment {
	d := deployment(replicas, rollingUpdateStrategy(nil))
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: podLabels()}
	return d
}

func runUsageCheck(t *testing.T, d *appsv1.Deployment, metricsClient *metricsfake.Clientset, objs ...runtime.Object) check.Result {
	t.Helper()
	all := append([]runtime.Object{d}, objs...)
	client := fake.NewSimpleClientset(all...)

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}
	if metricsClient != nil {
		target.MetricsClient = metricsClient.MetricsV1beta1()
	}

	res, err := check.RequestsVsUsage{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

func TestRequestsVsUsage_MetricsClientNil_Skipped(t *testing.T) {
	res := runUsageCheck(t, deploymentWithSelector(1), nil)

	if !res.Skipped {
		t.Fatal("want Skipped=true when MetricsClient is nil")
	}
	if res.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

func TestRequestsVsUsage_NoPods_NoFindings(t *testing.T) {
	metricsClient := metricsfake.NewSimpleClientset()
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient)

	if res.Skipped {
		t.Fatalf("no pods running yet is not an inability to evaluate, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings without pods, got %+v", res.Findings)
	}
}

func TestRequestsVsUsage_UsageWithinRequests_NoFindings(t *testing.T) {
	pod := podWithRequests("app-1", "500m", "512Mi")
	metricsClient := metricsClientWith(podMetrics("app-1", "200m", "300Mi"))
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient, pod)

	if len(res.Findings) != 0 {
		t.Fatalf("usage within requests: want 0 findings, got %+v", res.Findings)
	}
}

func TestRequestsVsUsage_CPUAboveRequest_Medium(t *testing.T) {
	pod := podWithRequests("app-1", "200m", "512Mi")
	metricsClient := metricsClientWith(podMetrics("app-1", "500m", "300Mi"))
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient, pod)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for CPU above request, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium", f.Severity)
	}
	if f.CheckID != check.RequestsVsUsageCheckID {
		t.Errorf("checkID = %q, want %q", f.CheckID, check.RequestsVsUsageCheckID)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
}

func TestRequestsVsUsage_MemoryAboveRequest_Medium(t *testing.T) {
	pod := podWithRequests("app-1", "500m", "128Mi")
	metricsClient := metricsClientWith(podMetrics("app-1", "200m", "256Mi"))
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient, pod)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for memory above request, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium", res.Findings[0].Severity)
	}
}

func TestRequestsVsUsage_ContainerWithoutRequest_Ignored(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: testNamespace, Labels: podLabels()},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}
	metricsClient := metricsClientWith(podMetrics("app-1", "500m", "512Mi"))
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient, pod)

	if len(res.Findings) != 0 {
		t.Fatalf("a container without requests must not produce findings here, got %+v", res.Findings)
	}
}

func TestRequestsVsUsage_PodListFailed_Skipped(t *testing.T) {
	d := deploymentWithSelector(1)
	client := fake.NewSimpleClientset(d)
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading pods")
	})

	target := check.Target{
		Namespace:     testNamespace,
		Workload:      workload.FromDeployment(d),
		Client:        client,
		MetricsClient: metricsfake.NewSimpleClientset().MetricsV1beta1(),
	}

	res, err := check.RequestsVsUsage{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed pod list; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatal("want Skipped=true when the pod list is not accessible")
	}
}

func TestRequestsVsUsage_PodMetricsFailed_Skipped(t *testing.T) {
	pod := podWithRequests("app-1", "500m", "512Mi")
	d := deploymentWithSelector(1)
	client := fake.NewSimpleClientset(d, pod)

	metricsClient := metricsfake.NewSimpleClientset()
	metricsClient.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("metrics-server is unreachable")
	})

	target := check.Target{
		Namespace:     testNamespace,
		Workload:      workload.FromDeployment(d),
		Client:        client,
		MetricsClient: metricsClient.MetricsV1beta1(),
	}

	res, err := check.RequestsVsUsage{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() must not return an error when metrics are not accessible; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatal("want Skipped=true when pod metrics are not accessible")
	}
}
