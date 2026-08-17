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
		// Labels replica quelle del pod sorgente: un metrics-server reale
		// le copia sul PodMetrics, ed e' quello che permette alla List
		// con LabelSelector (usata da RequestsVsUsage per restringere ai
		// pod del workload) di trovarlo. Senza, il fake clientset generato
		// (gentype.FakeClientWithList) filtra via selector lato client
		// sugli oggetti restituiti dai reactor e scarterebbe un oggetto
		// senza Labels indipendentemente dal reactor.
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

// metricsClientWith costruisce un fake metrics clientset che risponde
// alla List di PodMetrics con gli item dati. Non si puo' seminare
// PodMetrics passandoli a metricsfake.NewSimpleClientset: l'ObjectTracker
// generico deduce la risorsa dal Kind con una pluralizzazione naive
// ("PodMetrics" -> qualcosa come "podmetricses"), ma il client tipizzato
// generato usa davvero la risorsa "pods" nel gruppo metrics.k8s.io (vedi
// k8s.io/metrics/.../typed/metrics/v1beta1/fake/fake_podmetrics.go,
// WithResource("pods")): i due non si incontrano mai, la List
// risulterebbe sempre vuota. Un reactor che risponde direttamente sulla
// risorsa "pods" bypassa il problema.
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
		t.Fatalf("Run() ha restituito un errore inatteso: %v", err)
	}
	return res
}

func TestRequestsVsUsage_MetricsClientNil_Skipped(t *testing.T) {
	res := runUsageCheck(t, deploymentWithSelector(1), nil)

	if !res.Skipped {
		t.Fatal("atteso Skipped=true quando MetricsClient e' nil")
	}
	if res.SkipReason == "" {
		t.Error("SkipReason non deve essere vuoto")
	}
}

func TestRequestsVsUsage_NessunPod_NessunFinding(t *testing.T) {
	metricsClient := metricsfake.NewSimpleClientset()
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient)

	if res.Skipped {
		t.Fatalf("nessun pod ancora in esecuzione non e' un'impossibilita' di valutare, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("attesi 0 finding senza pod, got %+v", res.Findings)
	}
}

func TestRequestsVsUsage_UsoEntroLeRequest_NessunFinding(t *testing.T) {
	pod := podWithRequests("app-1", "500m", "512Mi")
	metricsClient := metricsClientWith(podMetrics("app-1", "200m", "300Mi"))
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient, pod)

	if len(res.Findings) != 0 {
		t.Fatalf("uso entro le request, attesi 0 finding, got %+v", res.Findings)
	}
}

func TestRequestsVsUsage_CPUOltreLaRequest_Medium(t *testing.T) {
	pod := podWithRequests("app-1", "200m", "512Mi")
	metricsClient := metricsClientWith(podMetrics("app-1", "500m", "300Mi"))
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient, pod)

	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding per cpu oltre la request, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity = %v, atteso Medium", f.Severity)
	}
	if f.CheckID != check.RequestsVsUsageCheckID {
		t.Errorf("checkID = %q, atteso %q", f.CheckID, check.RequestsVsUsageCheckID)
	}
	if !f.Remediation.ContextDependent {
		t.Error("la remediation deve dichiararsi context-dependent")
	}
}

func TestRequestsVsUsage_MemoriaOltreLaRequest_Medium(t *testing.T) {
	pod := podWithRequests("app-1", "500m", "128Mi")
	metricsClient := metricsClientWith(podMetrics("app-1", "200m", "256Mi"))
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient, pod)

	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding per memoria oltre la request, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityMedium {
		t.Errorf("severity = %v, atteso Medium", res.Findings[0].Severity)
	}
}

func TestRequestsVsUsage_ContainerSenzaRequest_Ignorato(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: testNamespace, Labels: podLabels()},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}
	metricsClient := metricsClientWith(podMetrics("app-1", "500m", "512Mi"))
	res := runUsageCheck(t, deploymentWithSelector(1), metricsClient, pod)

	if len(res.Findings) != 0 {
		t.Fatalf("un container senza request non deve produrre finding qui, got %+v", res.Findings)
	}
}

func TestRequestsVsUsage_ListaPodFallita_Skipped(t *testing.T) {
	d := deploymentWithSelector(1)
	client := fake.NewSimpleClientset(d)
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC nega la lettura dei pod")
	})

	target := check.Target{
		Namespace:     testNamespace,
		Workload:      workload.FromDeployment(d),
		Client:        client,
		MetricsClient: metricsfake.NewSimpleClientset().MetricsV1beta1(),
	}

	res, err := check.RequestsVsUsage{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() non deve restituire errore su lista pod fallita, deve degradare a Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatal("atteso Skipped=true quando la lista dei pod non e' accessibile")
	}
}

func TestRequestsVsUsage_MetrichePodFallite_Skipped(t *testing.T) {
	pod := podWithRequests("app-1", "500m", "512Mi")
	d := deploymentWithSelector(1)
	client := fake.NewSimpleClientset(d, pod)

	metricsClient := metricsfake.NewSimpleClientset()
	metricsClient.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("metrics-server non raggiungibile")
	})

	target := check.Target{
		Namespace:     testNamespace,
		Workload:      workload.FromDeployment(d),
		Client:        client,
		MetricsClient: metricsClient.MetricsV1beta1(),
	}

	res, err := check.RequestsVsUsage{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() non deve restituire errore su metriche non accessibili, deve degradare a Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatal("atteso Skipped=true quando le metriche pod non sono accessibili")
	}
}
