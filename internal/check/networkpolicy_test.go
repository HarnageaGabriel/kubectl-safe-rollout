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
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func denyAllIngressPolicy(name string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podLabels()},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			// No Ingress rules: deny-all when this is the only matching
			// ingress-affecting Policy.
		},
	}
}

func allowIngressPolicy(name string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podLabels()},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{}}, // allow-all-sources rule
		},
	}
}

func runNetworkPolicyIngressCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.NetworkPolicyIngress{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestNetworkPolicyIngress_NoPolicies_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})

	result := runNetworkPolicyIngressCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no NetworkPolicy at all: want empty result, got %+v", result)
	}
}

func TestNetworkPolicyIngress_DenyAllNoOtherPolicy_HighFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	np := denyAllIngressPolicy("deny-all")

	result := runNetworkPolicyIngressCheck(t, d, np)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the deny-all policy, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.NetworkPolicyIngressCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "deny-all") {
		t.Errorf("cause must name the restricting NetworkPolicy, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
}

// The critical case: NetworkPolicies are additive. A deny-all baseline
// alongside a policy that explicitly allows ingress must NOT be flagged —
// this is the standard, correct "default-deny plus allowlist" idiom, not
// a misconfiguration. Getting this wrong would make the check actively
// harmful against the most common intentional NetworkPolicy pattern.
func TestNetworkPolicyIngress_DenyAllPlusAllowPolicy_NoFindings(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	deny := denyAllIngressPolicy("deny-all")
	allow := allowIngressPolicy("allow-from-gateway")

	result := runNetworkPolicyIngressCheck(t, d, deny, allow)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("a deny-all baseline plus an explicit allow policy is the correct default-deny idiom, not a break: want empty result, got %+v", result)
	}
}

func TestNetworkPolicyIngress_NonMatchingSelector_Ignored(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "deny-other", Namespace: testNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "unrelated"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}

	result := runNetworkPolicyIngressCheck(t, d, np)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("NetworkPolicy selects a different workload: want empty result, got %+v", result)
	}
}

func TestNetworkPolicyIngress_EgressOnlyPolicy_Ignored(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "egress-only", Namespace: testNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podLabels()},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			// No Ingress section: irrelevant, since PolicyTypes excludes
			// Ingress entirely — this Policy must not restrict ingress.
		},
	}

	result := runNetworkPolicyIngressCheck(t, d, np)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("an Egress-only Policy must not be treated as restricting ingress, got %+v", result)
	}
}

func TestNetworkPolicyIngress_UnsetPolicyTypesEmptyRules_HighFinding(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "deny-all-implicit", Namespace: testNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podLabels()},
			// PolicyTypes deliberately left unset: Kubernetes defaults an
			// unset PolicyTypes to affecting Ingress.
		},
	}

	result := runNetworkPolicyIngressCheck(t, d, np)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("unset PolicyTypes defaults to affecting Ingress in Kubernetes itself: want one finding, got %+v", result)
	}
}

func TestNetworkPolicyIngress_ListFailed_Skipped(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "networkpolicies", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing NetworkPolicies")
	})

	result, err := check.NetworkPolicyIngress{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the NetworkPolicy list is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
