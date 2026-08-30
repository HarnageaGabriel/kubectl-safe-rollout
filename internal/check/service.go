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
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ServiceRoutingCheckID is the stable identifier for this check.
const ServiceRoutingCheckID = "service-routing"

// ServiceRouting verifies that Services selecting this workload's pods can
// actually route traffic to them. Every other check in this package looks
// only at the workload itself: a rollout can pass all of them and still
// leave a Service in front of it silently dropping every request, and
// nothing here would notice unless this check exists. Two independent,
// verified (not heuristic) facts are checked, both severity High because
// each is a guaranteed break, not an eroded margin:
//
//   - a Service port with a named targetPort that no container in the pod
//     template declares: Kubernetes cannot resolve the port number, so
//     matching pods are never added as endpoints for that port;
//   - a Service whose selector matches this workload's pods, at least one
//     of which is already Ready, yet whose EndpointSlices carry zero ready
//     addresses.
type ServiceRouting struct{}

// ID implements check.Check.
func (ServiceRouting) ID() string { return ServiceRoutingCheckID }

// Run implements check.Check.
func (c ServiceRouting) Run(ctx context.Context, target Target) (Result, error) {
	svcList, err := target.Client.CoreV1().Services(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("service list is not accessible: %v", err)), nil
	}

	matching := matchingServices(svcList.Items, target.Workload.PodLabels())
	if len(matching) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	containerPorts := declaredPortNames(target.Workload.PodContainers())

	var findings []model.Finding
	for _, svc := range matching {
		findings = append(findings, namedPortFindings(svc, containerPorts, workloadRef)...)
	}

	selector, err := target.Workload.PodSelector()
	if err != nil {
		return Result{CheckID: c.ID(), Findings: findings}, nil
	}
	podList, err := target.Client.CoreV1().Pods(target.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		// Best effort: the named-port findings collected above are
		// verified independently of live pods and remain valid; only the
		// live-endpoint comparison below is skipped.
		return Result{CheckID: c.ID(), Findings: findings}, nil
	}
	readyPods := countReadyPods(podList.Items)
	if readyPods == 0 {
		// Nothing should be routable yet either way: whatever keeps pods
		// from becoming Ready is a pod-level problem another check or
		// `watch` already covers, not evidence of a Service misconfiguration.
		return Result{CheckID: c.ID(), Findings: findings}, nil
	}

	for _, svc := range matching {
		if f, ok := zeroEndpointsFinding(ctx, target, svc, readyPods, workloadRef); ok {
			findings = append(findings, f)
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// matchingServices returns the Services whose selector actually matches
// podLabels, i.e. the Services that front the workload those labels come
// from. Shared with ingress-routing, which needs the same set to know
// which Ingress backends are worth checking.
func matchingServices(services []corev1.Service, podLabels map[string]string) []corev1.Service {
	labelSet := labels.Set(podLabels)
	var matching []corev1.Service
	for _, svc := range services {
		if len(svc.Spec.Selector) == 0 {
			// Headless/manually-managed Endpoints: nothing here selects
			// this workload's pods by label, so there is no routing
			// contract for this check to verify.
			continue
		}
		if labels.SelectorFromSet(svc.Spec.Selector).Matches(labelSet) {
			matching = append(matching, svc)
		}
	}
	return matching
}

func declaredPortNames(containers []corev1.Container) map[string]bool {
	names := make(map[string]bool)
	for _, c := range containers {
		for _, p := range c.Ports {
			if p.Name != "" {
				names[p.Name] = true
			}
		}
	}
	return names
}

func namedPortFindings(svc corev1.Service, containerPorts map[string]bool, workloadRef string) []model.Finding {
	var findings []model.Finding
	for _, port := range svc.Spec.Ports {
		if port.TargetPort.Type != intstr.String {
			continue
		}
		name := port.TargetPort.StrVal
		if containerPorts[name] {
			continue
		}
		findings = append(findings, model.Finding{
			CheckID:  ServiceRoutingCheckID,
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"Service %q selects %s but its port %q targets container port %q, which no container in the pod template declares: Kubernetes cannot resolve the port number and never adds these pods as endpoints for it",
				svc.Name, workloadRef, port.Name, name,
			),
			Evidence: []string{fmt.Sprintf("service=%s port=%s targetPort=%s", svc.Name, port.Name, name)},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("declare a containerPort named %q in the pod template, or correct Service %q's targetPort to an existing one", name, svc.Name),
				ContextDependent: true,
			},
			Resource: model.ResourceRef{Kind: "Service", Namespace: svc.Namespace, Name: svc.Name},
		})
	}
	return findings
}

func countReadyPods(pods []corev1.Pod) int {
	count := 0
	for _, p := range pods {
		if p.DeletionTimestamp != nil {
			// The endpoint controller stops counting a pod the moment it
			// starts terminating, but the kubelet does not necessarily
			// flip its own PodReady condition to false at that same
			// instant: without this guard, a single-replica rolling
			// update would read as a false positive during the brief
			// window where the old pod is terminating (already dropped
			// from EndpointSlice) but still reports Ready=true.
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				count++
				break
			}
		}
	}
	return count
}

func zeroEndpointsFinding(ctx context.Context, target Target, svc corev1.Service, readyPods int, workloadRef string) (model.Finding, bool) {
	slices, err := target.Client.DiscoveryV1().EndpointSlices(svc.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + svc.Name,
	})
	if err != nil {
		// Best effort: an unreadable EndpointSlice list is not proof of a
		// problem, so this Service is silently skipped rather than
		// reported on incomplete evidence.
		return model.Finding{}, false
	}
	ready := 0
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			// Ready == nil is defined by the API as "unknown", which
			// consumers are expected to treat as ready.
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				ready += len(ep.Addresses)
			}
		}
	}
	if ready > 0 {
		return model.Finding{}, false
	}
	return model.Finding{
		CheckID:  ServiceRoutingCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"Service %q selects %s, which has %d ready pod(s), but the Service has zero ready endpoints: traffic sent to it is dropped",
			svc.Name, workloadRef, readyPods,
		),
		Evidence: []string{fmt.Sprintf("service=%s readyPods=%d endpoints=0", svc.Name, readyPods)},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("compare Service %q's selector and port definitions against the pod template's labels and container ports; if they match, inspect its EndpointSlices for further detail", svc.Name),
			Commands:         []string{fmt.Sprintf("kubectl get endpointslice -n %s -l kubernetes.io/service-name=%s", svc.Namespace, svc.Name)},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{Kind: "Service", Namespace: svc.Namespace, Name: svc.Name},
	}, true
}
