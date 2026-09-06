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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// IngressClassExistsCheckID is the stable identifier for this check.
const IngressClassExistsCheckID = "ingressclass-exists"

// IngressClassExists checks that a relevant Ingress's spec.ingressClassName,
// when set, actually resolves to an IngressClass object. IngressClass is
// cluster-scoped, so this is a single Get by name — a verified fact, not a
// heuristic, the same shape already used by serviceaccount-exists and
// priorityclass-exists.
//
// This is a separate check from ingress-routing, not folded into it, for
// the same reason hpa-quota-headroom is kept separate from quota-headroom:
// it verifies a different fact (does the named IngressClass object exist)
// from what ingress-routing already checks (does the backend Service/port/
// TLS Secret resolve). Mixing the two into one CheckID would make a
// passing or failing ingress-routing result ambiguous about which question
// it actually answered.
//
// Relevance uses the exact same definition as ingress-routing
// (relevantIngresses): only Ingresses whose DefaultBackend or a rule
// backend names a Service fronting this workload are considered. An
// Ingress unrelated to this workload is out of scope even if its
// ingressClassName is broken.
//
// Unset/nil ingressClassName is not flagged: it is a legitimate, common
// configuration — a single-controller cluster with no IngressClass objects
// at all, reliance on the deprecated kubernetes.io/ingress.class
// annotation, or a controller-specific default-class mechanism — not a
// misconfiguration. Flagging it would be a false positive on a common,
// healthy setup, the same reasoning already applied to an unset
// priorityClassName in priorityclass-exists.
//
// The deprecated kubernetes.io/ingress.class annotation is explicitly out
// of scope: this check only resolves spec.ingressClassName, the field this
// project's own manifests and the modern networking.k8s.io/v1 API use.
//
// Severity High: if the name does not resolve, no ingress controller
// implements that Ingress at all — it is silently inert, with no error
// surfaced anywhere in kubectl get/describe, the same invisible-failure
// class this project already covers for ServiceAccount and PriorityClass.
type IngressClassExists struct{}

// ID implements check.Check.
func (IngressClassExists) ID() string { return IngressClassExistsCheckID }

// Run implements check.Check.
func (c IngressClassExists) Run(ctx context.Context, target Target) (Result, error) {
	svcList, err := target.Client.CoreV1().Services(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("service list is not accessible: %v", err)), nil
	}
	matching := matchingServices(svcList.Items, target.Workload.PodLabels())
	if len(matching) == 0 {
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
	for _, ing := range relevantIngresses(ingList.Items, servicesByName) {
		name := ing.Spec.IngressClassName
		if name == nil || *name == "" {
			continue
		}
		_, err := target.Client.NetworkingV1().IngressClasses().Get(ctx, *name, metav1.GetOptions{})
		switch {
		case err == nil:
			continue
		case apierrors.IsNotFound(err):
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityHigh,
				Cause: fmt.Sprintf(
					"Ingress %q references IngressClass %q, which does not exist: no ingress controller implements this Ingress, and nothing in kubectl get/describe surfaces the failure",
					ing.Name, *name,
				),
				Evidence: []string{fmt.Sprintf("ingress=%s ingressClassName=%s", ing.Name, *name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("create IngressClass %q, or correct Ingress %q's ingressClassName to one actually installed in the cluster", *name, ing.Name),
					Commands:         []string{fmt.Sprintf("kubectl get ingressclass %s", *name), "kubectl get ingressclasses"},
					ContextDependent: true,
				},
				Resource: model.ResourceRef{Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name},
			})
		default:
			// One Ingress's inaccessible IngressClass is not proof that any
			// other relevant Ingress's IngressClass is missing too, but it
			// also means this check cannot tell "exists" from "does not
			// exist" for the rest of the run: rather than reporting a
			// partial, possibly misleading set of findings, the whole
			// check degrades to Skip, matching how a failed List already
			// degrades the entire check elsewhere in this file.
			return Skip(c.ID(), fmt.Sprintf("failed to read IngressClass %q: %v", *name, err)), nil
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}
