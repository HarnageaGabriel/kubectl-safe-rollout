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
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// NetworkPolicyIngressCheckID is the stable identifier for this check.
const NetworkPolicyIngressCheckID = "network-policy-ingress"

// NetworkPolicyIngress checks whether every NetworkPolicy restricting
// ingress traffic to this workload's pods, taken together, permits zero
// incoming traffic — leaving the workload unreachable from the network
// regardless of how healthy its Service and Ingress are. The last, and
// outermost, layer of the traffic-path family alongside service-routing
// and ingress-routing.
//
// NetworkPolicies are additive across the whole set that selects a pod: a
// Policy with an empty Ingress rule list does not override another
// matching Policy's explicit allow rules, it only contributes nothing of
// its own. The idiomatic "default-deny baseline plus an explicit allow
// policy" pattern relies on exactly this union semantics, and is the
// correct, intended security posture — not a misconfiguration. This check
// therefore only fires when the union of Ingress rules across every
// matching, ingress-affecting Policy is empty. That emptiness is a fact
// guaranteed by the NetworkPolicy API contract itself, true regardless of
// which CNI enforces it — unlike, for example, whether a probe request
// actually reaches a pod, which genuinely does vary by CNI and is
// deliberately not something this project claims to verify.
type NetworkPolicyIngress struct{}

// ID implements check.Check.
func (NetworkPolicyIngress) ID() string { return NetworkPolicyIngressCheckID }

// Run implements check.Check.
func (c NetworkPolicyIngress) Run(ctx context.Context, target Target) (Result, error) {
	npList, err := target.Client.NetworkingV1().NetworkPolicies(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("NetworkPolicy list is not accessible: %v", err)), nil
	}

	podLabels := labels.Set(target.Workload.PodLabels())
	var matching []networkingv1.NetworkPolicy
	totalIngressRules := 0
	for _, np := range npList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
		if err != nil || !selector.Matches(podLabels) {
			continue
		}
		if !affectsIngress(np) {
			// An Egress-only Policy (explicit policyTypes: [Egress]) does
			// not restrict ingress at all, whatever its (ignored) Ingress
			// field might otherwise contain.
			continue
		}
		matching = append(matching, np)
		totalIngressRules += len(np.Spec.Ingress)
	}
	if len(matching) == 0 || totalIngressRules > 0 {
		return Result{CheckID: c.ID()}, nil
	}

	names := make([]string, len(matching))
	for i, np := range matching {
		names[i] = np.Name
	}
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	return Result{CheckID: c.ID(), Findings: []model.Finding{{
		CheckID:  c.ID(),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%s is selected by %d NetworkPolicy object(s) restricting ingress (%s), and none of them permits any incoming traffic: the workload is unreachable from the network regardless of Service or Ingress health",
			workloadRef, len(matching), strings.Join(names, ", "),
		),
		Evidence: []string{
			fmt.Sprintf("matchingNetworkPolicies=%s", strings.Join(names, ",")),
			"totalIngressRules=0",
		},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("add an Ingress rule to one of %s permitting the traffic %s actually needs to receive, or remove the policy if it was applied unintentionally", strings.Join(names, ", "), workloadRef),
			Commands:         []string{fmt.Sprintf("kubectl get networkpolicy -n %s -o yaml", target.Namespace)},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{Kind: target.Workload.Kind(), Namespace: target.Namespace, Name: target.Workload.Name()},
	}}}, nil
}

// affectsIngress reports whether np restricts ingress traffic at all,
// applying the same default Kubernetes itself applies: a Policy with no
// PolicyTypes set is assumed to affect Ingress regardless of its Ingress
// field's contents.
func affectsIngress(np networkingv1.NetworkPolicy) bool {
	if len(np.Spec.PolicyTypes) == 0 {
		return true
	}
	for _, t := range np.Spec.PolicyTypes {
		if t == networkingv1.PolicyTypeIngress {
			return true
		}
	}
	return false
}
