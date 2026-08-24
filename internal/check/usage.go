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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// RequestsVsUsageCheckID is the stable identifier for this check.
const RequestsVsUsageCheckID = "requests-vs-usage"

// RequestsVsUsage compares the actual usage of the workload's current pods
// (read from metrics-server) with the requests declared in the pod template.
// Requests that understate actual usage pose a concrete but nondeterministic
// rollout risk: during surge, new pods are scheduled on the assumption that
// existing pods consume what they declared, not what they actually consume.
// Severity Medium, not High: unlike `quota-headroom` or `pdb-consistency`,
// there is no calculation here that guarantees the rollout will be blocked,
// only an eroded safety margin.
//
// It compares only resources for which a request is actually declared: a
// container without a request for a resource is the responsibility of a
// different check (presence of requests/limits, not yet implemented), not
// this one.
type RequestsVsUsage struct{}

// ID implements check.Check.
func (RequestsVsUsage) ID() string { return RequestsVsUsageCheckID }

// Run implements check.Check.
func (c RequestsVsUsage) Run(ctx context.Context, target Target) (Result, error) {
	if target.MetricsClient == nil {
		return Skip(c.ID(), "metrics-server is unreachable: metrics client is unavailable"), nil
	}

	selector, err := target.Workload.PodSelector()
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("invalid pod selector: %v", err)), nil
	}
	listOpts := metav1.ListOptions{LabelSelector: selector.String()}

	podList, err := target.Client.CoreV1().Pods(target.Namespace).List(ctx, listOpts)
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("pod list is not accessible: %v", err)), nil
	}
	if len(podList.Items) == 0 {
		// No pods are running yet (for example, the first rollout has
		// not started): there is nothing to compare, not an inability
		// to evaluate.
		return Result{CheckID: c.ID()}, nil
	}

	metricsList, err := target.MetricsClient.PodMetricses(target.Namespace).List(ctx, listOpts)
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("pod metrics are not accessible (metrics-server absent or not ready yet): %v", err)), nil
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

// formatQuantity re-renders a resource.Quantity in the scale a human expects
// for that resource, instead of whatever scale the value happened to arrive
// in. Declared requests come from a parsed pod spec (e.g. "1m"), and usage
// comes from the Metrics API, which reports CPU in raw nanocores and can
// leave a Quantity's internal Format such that String() prints it that way
// (observed on kind: "usage=500659974n" next to "request=1m" in the same
// evidence line, forcing the reader to convert units to see that usage is
// roughly 500x the request). Re-normalizing both sides to the same unit is
// what makes the comparison readable at a glance, which is the whole point
// of evidence meant to be read during an incident.
func formatQuantity(name corev1.ResourceName, q resource.Quantity) string {
	if name == corev1.ResourceCPU {
		return resource.NewMilliQuantity(q.MilliValue(), resource.DecimalSI).String()
	}
	return resource.NewQuantity(q.Value(), resource.BinarySI).String()
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
			evidence = append(evidence, fmt.Sprintf("%s: request=%s usage=%s", resName, formatQuantity(resName, req), formatQuantity(resName, use)))
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
			"container %q in pod %q (workload %s) uses more resources than it requested: rolling update surge underestimates the actual node capacity required",
			containerName, podName, workloadRef,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("increase the requests for container %q to at least the observed usage; the exact value depends on the actual load profile over time, not just this sample", containerName),
			ContextDependent: true,
		},
		Resource: resource,
	}, true
}
