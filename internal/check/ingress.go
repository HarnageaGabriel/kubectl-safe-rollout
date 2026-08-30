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
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// IngressRoutingCheckID is the stable identifier for this check.
const IngressRoutingCheckID = "ingress-routing"

// IngressRouting extends service-routing one hop further out: an Ingress
// backend names a Service and, optionally, one of that Service's ports by
// name or number. If that reference does not resolve, the Ingress
// controller cannot route to the Service at all — a break external
// traffic hits even though the Service itself is perfectly healthy and
// service-routing would report nothing wrong. Severity High: like
// service-routing, this is a verified fact about a live object
// cross-reference, not a heuristic.
type IngressRouting struct{}

// ID implements check.Check.
func (IngressRouting) ID() string { return IngressRoutingCheckID }

// Run implements check.Check.
func (c IngressRouting) Run(ctx context.Context, target Target) (Result, error) {
	svcList, err := target.Client.CoreV1().Services(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("service list is not accessible: %v", err)), nil
	}
	matching := matchingServices(svcList.Items, target.Workload.PodLabels())
	if len(matching) == 0 {
		// No Service fronts this workload at all: whether that itself is
		// worth flagging is service-routing's concern, not this one's.
		return Result{CheckID: c.ID()}, nil
	}
	servicesByName := make(map[string]corev1.Service, len(matching))
	for _, svc := range matching {
		servicesByName[svc.Name] = svc
	}

	ingList, err := target.Client.NetworkingV1().Ingresses(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("ingress list is not accessible: %v", err)), nil
	}

	var findings []model.Finding
	for _, ing := range ingList.Items {
		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			findings = append(findings, backendFindings(ing, servicesByName, *ing.Spec.DefaultBackend.Service)...)
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service == nil {
					continue
				}
				findings = append(findings, backendFindings(ing, servicesByName, *path.Backend.Service)...)
			}
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

func backendFindings(ing netv1.Ingress, servicesByName map[string]corev1.Service, backend netv1.IngressServiceBackend) []model.Finding {
	svc, ok := servicesByName[backend.Name]
	if !ok {
		// Not a Service that fronts this workload: out of scope for this
		// check, whether or not the Service exists at all.
		return nil
	}

	if backend.Port.Name != "" && !hasPortName(svc, backend.Port.Name) {
		return []model.Finding{{
			CheckID:  IngressRoutingCheckID,
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"Ingress %q routes to Service %q port %q, which the Service does not declare: the ingress controller cannot resolve this backend",
				ing.Name, svc.Name, backend.Port.Name,
			),
			Evidence: []string{fmt.Sprintf("ingress=%s service=%s port=%s", ing.Name, svc.Name, backend.Port.Name)},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("correct Ingress %q's backend port name to one Service %q actually declares, or add that port to the Service", ing.Name, svc.Name),
				ContextDependent: true,
			},
			Resource: model.ResourceRef{Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name},
		}}
	}

	if backend.Port.Number != 0 && !hasPortNumber(svc, backend.Port.Number) {
		return []model.Finding{{
			CheckID:  IngressRoutingCheckID,
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"Ingress %q routes to Service %q port %d, which the Service does not declare: the ingress controller cannot resolve this backend",
				ing.Name, svc.Name, backend.Port.Number,
			),
			Evidence: []string{fmt.Sprintf("ingress=%s service=%s port=%d", ing.Name, svc.Name, backend.Port.Number)},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("correct Ingress %q's backend port to one Service %q actually declares, or add that port to the Service", ing.Name, svc.Name),
				ContextDependent: true,
			},
			Resource: model.ResourceRef{Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name},
		}}
	}

	return nil
}

func hasPortName(svc corev1.Service, name string) bool {
	for _, p := range svc.Spec.Ports {
		if p.Name == name {
			return true
		}
	}
	return false
}

func hasPortNumber(svc corev1.Service, number int32) bool {
	for _, p := range svc.Spec.Ports {
		if p.Port == number {
			return true
		}
	}
	return false
}
