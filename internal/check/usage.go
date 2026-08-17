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

package check

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// RequestsVsUsageCheckID e' l'identificativo stabile di questa verifica.
const RequestsVsUsageCheckID = "requests-vs-usage"

// RequestsVsUsage confronta l'uso reale dei pod correnti del workload
// (letto da metrics-server) con le request dichiarate nel pod template.
// Request sottostimate rispetto all'uso reale sono un rischio concreto
// ma non deterministico per il rollout: durante il surge, i pod nuovi
// vengono schedulati assumendo che quelli esistenti consumino quanto
// dichiarato, non quanto consumano davvero. Severity Medium, non High:
// a differenza di `quota-headroom` o `pdb-consistency`, qui non c'e' un
// calcolo che garantisce il blocco del rollout, solo un margine di
// sicurezza eroso.
//
// Confronta solo le risorse per cui una request e' effettivamente
// dichiarata: un container senza request per una risorsa e' compito di
// una verifica diversa (presenza di request/limits, non ancora
// implementata), non di questa.
type RequestsVsUsage struct{}

// ID implementa check.Check.
func (RequestsVsUsage) ID() string { return RequestsVsUsageCheckID }

// Run implementa check.Check.
func (c RequestsVsUsage) Run(ctx context.Context, target Target) (Result, error) {
	if target.MetricsClient == nil {
		return Skip(c.ID(), "metrics-server non raggiungibile: client metrics non disponibile"), nil
	}

	selector, err := target.Workload.PodSelector()
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("selector pod non valido: %v", err)), nil
	}
	listOpts := metav1.ListOptions{LabelSelector: selector.String()}

	podList, err := target.Client.CoreV1().Pods(target.Namespace).List(ctx, listOpts)
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("lista pod non accessibile: %v", err)), nil
	}
	if len(podList.Items) == 0 {
		// Nessun pod ancora in esecuzione (es. primo rollout non
		// partito): niente da confrontare, non un'impossibilita' di
		// valutare.
		return Result{CheckID: c.ID()}, nil
	}

	metricsList, err := target.MetricsClient.PodMetricses(target.Namespace).List(ctx, listOpts)
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("metriche pod non accessibili (metrics-server assente o non ancora pronto): %v", err)), nil
	}

	requests := indexContainerRequests(podList.Items)
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	var findings []model.Finding
	for _, pm := range metricsList.Items {
		for _, cm := range pm.Containers {
			req, ok := requests[podContainerKey{pod: pm.Name, container: cm.Name}]
			if !ok {
				continue
			}
			if f, ok := usageExceedsRequestFinding(target.Namespace, pm.Name, cm.Name, req, cm.Usage, workloadRef); ok {
				findings = append(findings, f)
			}
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

type podContainerKey struct {
	pod       string
	container string
}

func indexContainerRequests(pods []corev1.Pod) map[podContainerKey]corev1.ResourceList {
	m := make(map[podContainerKey]corev1.ResourceList, len(pods))
	for _, p := range pods {
		for _, c := range p.Spec.Containers {
			m[podContainerKey{pod: p.Name, container: c.Name}] = c.Resources.Requests
		}
	}
	return m
}

func usageExceedsRequestFinding(namespace, podName, containerName string, requests, usage corev1.ResourceList, workloadRef string) (model.Finding, bool) {
	var evidence []string
	for _, resName := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		req, hasReq := requests[resName]
		use, hasUse := usage[resName]
		if !hasReq || !hasUse || req.IsZero() {
			continue
		}
		if use.Cmp(req) > 0 {
			evidence = append(evidence, fmt.Sprintf("%s: request=%s uso=%s", resName, req.String(), use.String()))
		}
	}
	if len(evidence) == 0 {
		return model.Finding{}, false
	}
	resource := model.ResourceRef{Kind: "Pod", Namespace: namespace, Name: podName}
	return model.Finding{
		CheckID:  RequestsVsUsageCheckID,
		Severity: model.SeverityMedium,
		Cause: fmt.Sprintf(
			"il container %q del pod %q (workload %s) usa piu' risorse di quante ne abbia richieste: il surge del rolling update sottostima lo spazio reale necessario su nodo",
			containerName, podName, workloadRef,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("aumenta le request del container %q ad almeno l'uso osservato; il valore esatto dipende dal profilo di carico reale nel tempo, non solo da questo campione", containerName),
			ContextDependent: true,
		},
		Resource: resource,
	}, true
}
