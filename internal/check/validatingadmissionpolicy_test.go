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

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func vapPolicy(name string, spec admissionregistrationv1.ValidatingAdmissionPolicySpec) *admissionregistrationv1.ValidatingAdmissionPolicy {
	return &admissionregistrationv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
}

func vapBinding(name, policyName string, spec admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec) *admissionregistrationv1.ValidatingAdmissionPolicyBinding {
	spec.PolicyName = policyName
	return &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
}

// podCreateMatchConstraints builds a MatchResources that matches this
// project's fixture pod's CREATE admission, reusing podCreateRule already
// defined in admissionwebhook_test.go (same check_test package).
func podCreateMatchConstraints() *admissionregistrationv1.MatchResources {
	return &admissionregistrationv1.MatchResources{
		ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
			RuleWithOperations: podCreateRule(),
		}},
	}
}

func runVAPVisibilityCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.ValidatingAdmissionPolicyVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

// 1. matchConstraints match, binding matches, validationActions include
// Deny -> F1, Low, one finding per (policy, binding) pair.
func TestValidatingAdmissionPolicyVisibility_F1_LowFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("deny-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
		Validations:      []admissionregistrationv1.Validation{{Expression: "false", Message: "must not be false"}},
	})
	binding := vapBinding("deny-binding", "deny-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.ValidatingAdmissionPolicyVisibilityCheckID || f.Severity != model.SeverityLow {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want Low", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "deny-policy") || !strings.Contains(f.Cause, "deny-binding") {
		t.Errorf("cause must name the policy and binding, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
	if f.Resource.Kind != "ValidatingAdmissionPolicy" || f.Resource.Name != "deny-policy" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
	joined := strings.Join(f.Evidence, " ")
	if !strings.Contains(joined, "must not be false") {
		t.Errorf("evidence should include the validation message, got %+v", f.Evidence)
	}
}

// F1 must skip validations with no message rather than fabricate one.
func TestValidatingAdmissionPolicyVisibility_F1_SkipsValidationsWithoutMessage(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("mixed-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
		Validations: []admissionregistrationv1.Validation{
			{Expression: "true"},
			{Expression: "object.spec.x == 1", Message: "has a message"},
		},
	})
	binding := vapBinding("mixed-binding", "mixed-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	joined := strings.Join(result.Findings[0].Evidence, " ")
	if !strings.Contains(joined, "has a message") {
		t.Errorf("evidence must include the validation that has a message, got %+v", result.Findings[0].Evidence)
	}
	if strings.Contains(joined, "object.spec.x == 1") {
		t.Errorf("evidence must never fabricate a message from the CEL expression itself, got %+v", result.Findings[0].Evidence)
	}
}

// 2. F1 conditions plus paramKind set, binding has no paramRef, effective
// failurePolicy Fail -> F2, Medium.
func TestValidatingAdmissionPolicyVisibility_F2_MediumFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("param-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
		ParamKind:        &admissionregistrationv1.ParamKind{APIVersion: "example.com/v1", Kind: "PolicyConfig"},
		FailurePolicy:    failurePolicyPtr(admissionregistrationv1.Fail),
		Validations:      []admissionregistrationv1.Validation{{Expression: "params.data.foo == 'bar'"}},
	})
	binding := vapBinding("param-binding", "param-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity=%v, want Medium", f.Severity)
	}
	if !strings.Contains(f.Cause, "params") {
		t.Errorf("cause must explain the null-params mechanism, got %q", f.Cause)
	}
	joined := strings.Join(f.Evidence, " ")
	if !strings.Contains(joined, "PolicyConfig") {
		t.Errorf("evidence must include paramKind, got %+v", f.Evidence)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
}

// 3. validationActions containing only Warn/Audit (no Deny) -> no finding.
func TestValidatingAdmissionPolicyVisibility_WarnAuditOnly_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("warn-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
		Validations:      []admissionregistrationv1.Validation{{Expression: "false"}},
	})
	binding := vapBinding("warn-binding", "warn-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Warn, admissionregistrationv1.Audit},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("validationActions without Deny never blocks admission: want empty result, got %+v", result)
	}
}

// 4. binding names a policyName that does not exist -> silently ignored.
func TestValidatingAdmissionPolicyVisibility_DanglingPolicyName_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("real-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
	})
	binding := vapBinding("dangling-binding", "does-not-exist", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("a binding naming a nonexistent policy is silently ignored: want empty result, got %+v", result)
	}
}

// 5. excludeResourceRules matching -> excluded, no finding.
func TestValidatingAdmissionPolicyVisibility_ExcludeResourceRulesMatching_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("exclude-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionregistrationv1.MatchResources{
			ResourceRules:        []admissionregistrationv1.NamedRuleWithOperations{{RuleWithOperations: podCreateRule()}},
			ExcludeResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{RuleWithOperations: podCreateRule()}},
		},
	})
	binding := vapBinding("exclude-binding", "exclude-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("excludeResourceRules wins over resourceRules: want empty result, got %+v", result)
	}
}

// 6. matchConditions non-empty on the policy -> excluded from every
// conclusion, no finding.
func TestValidatingAdmissionPolicyVisibility_MatchConditionsPresent_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("cel-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
		MatchConditions: []admissionregistrationv1.MatchCondition{
			{Name: "only-prod", Expression: "object.metadata.namespace == 'prod'"},
		},
	})
	binding := vapBinding("cel-binding", "cel-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("a policy with matchConditions must be excluded from every conclusion: want empty result, got %+v", result)
	}
}

// 7. rule matches only deployments, not pods -> no finding.
func TestValidatingAdmissionPolicyVisibility_RuleCoversOnlyDeployments_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("deploy-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionregistrationv1.MatchResources{
			ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
				RuleWithOperations: admissionregistrationv1.RuleWithOperations{
					Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
					Rule:       admissionregistrationv1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1"}, Resources: []string{"deployments"}},
				},
			}},
		},
	})
	binding := vapBinding("deploy-binding", "deploy-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("rule does not cover pods: want empty result, got %+v", result)
	}
}

// 8. rule matches only UPDATE, not CREATE/* -> no finding.
func TestValidatingAdmissionPolicyVisibility_RuleCoversOnlyUpdate_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("update-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionregistrationv1.MatchResources{
			ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
				RuleWithOperations: admissionregistrationv1.RuleWithOperations{
					Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Update},
					Rule:       admissionregistrationv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}},
				},
			}},
		},
	})
	binding := vapBinding("update-binding", "update-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("rule covers only UPDATE: want empty result, got %+v", result)
	}
}

// 9. objectSelector does not match the target's PodLabels -> no finding.
func TestValidatingAdmissionPolicyVisibility_ObjectSelectorExcludes_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	mc := podCreateMatchConstraints()
	mc.ObjectSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"opt-in": "true"}}
	policy := vapPolicy("obj-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{MatchConstraints: mc})
	binding := vapBinding("obj-binding", "obj-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("objectSelector does not match the pod template's labels: want empty result, got %+v", result)
	}
}

// 10. namespaceSelector excludes the target namespace -> no finding.
func TestValidatingAdmissionPolicyVisibility_NamespaceSelectorExcludes_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	mc := podCreateMatchConstraints()
	mc.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}
	policy := vapPolicy("ns-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{MatchConstraints: mc})
	binding := vapBinding("ns-binding", "ns-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace, Labels: map[string]string{"env": "staging"}}}

	result := runVAPVisibilityCheck(t, d, policy, binding, ns)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("namespaceSelector excludes the target namespace: want empty result, got %+v", result)
	}
}

// 11. paramKind set with paramRef also set -> F1 still fires (Deny is
// present and CEL validations run independent of params), but never F2:
// params are not null.
func TestValidatingAdmissionPolicyVisibility_ParamRefSet_NoF2Elevation(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("param-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
		ParamKind:        &admissionregistrationv1.ParamKind{APIVersion: "example.com/v1", Kind: "PolicyConfig"},
		FailurePolicy:    failurePolicyPtr(admissionregistrationv1.Fail),
	})
	binding := vapBinding("param-binding", "param-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
		ParamRef:          &admissionregistrationv1.ParamRef{Name: "config"},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped F1 finding, got %+v", result)
	}
	if result.Findings[0].Severity != model.SeverityLow {
		t.Errorf("severity=%v, want Low: paramRef is set, so params is never null and F2 must not fire", result.Findings[0].Severity)
	}
}

// 12. paramKind set but effective failurePolicy is Ignore -> F1 still
// fires, but never F2: a CEL error does not deny under Ignore.
func TestValidatingAdmissionPolicyVisibility_FailurePolicyIgnore_NoF2Elevation(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("param-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
		ParamKind:        &admissionregistrationv1.ParamKind{APIVersion: "example.com/v1", Kind: "PolicyConfig"},
		FailurePolicy:    failurePolicyPtr(admissionregistrationv1.Ignore),
	})
	binding := vapBinding("param-binding", "param-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped F1 finding, got %+v", result)
	}
	if result.Findings[0].Severity != model.SeverityLow {
		t.Errorf("severity=%v, want Low: failurePolicy Ignore means a CEL error never denies, so F2 must not fire", result.Findings[0].Severity)
	}
}

// 13. a matching rule whose resourceNames is non-empty can never usefully
// match a controller-created (generated-name) pod -> no finding.
func TestValidatingAdmissionPolicyVisibility_ResourceNamesNonEmpty_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	rule := podCreateRule()
	policy := vapPolicy("names-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionregistrationv1.MatchResources{
			ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{
				ResourceNames:      []string{"specific-pod"},
				RuleWithOperations: rule,
			}},
		},
	})
	binding := vapBinding("names-binding", "names-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("a rule with non-empty resourceNames can never match a generated-name pod: want empty result, got %+v", result)
	}
}

// 14. binding sets matchResources but leaves resourceRules empty -> treated
// as "matches everything" (the easiest thing here to get backwards).
func TestValidatingAdmissionPolicyVisibility_BindingMatchResourcesEmptyResourceRules_TreatedAsMatching(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
	})
	binding := vapBinding("binding", "policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		MatchResources:    &admissionregistrationv1.MatchResources{}, // non-nil, resourceRules left unset
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	result := runVAPVisibilityCheck(t, d, policy, binding)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("an empty resourceRules on binding.matchResources must be treated as 'matches everything': want one finding, got %+v", result)
	}
}

// 15. no policies/bindings in the cluster -> no finding.
func TestValidatingAdmissionPolicyVisibility_NoPolicies_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})

	result := runVAPVisibilityCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no ValidatingAdmissionPolicy/Binding objects at all: want empty result, got %+v", result)
	}
}

// 16. List(ValidatingAdmissionPolicy) Forbidden -> Skip naming the
// resource, distinct from the API-not-served case.
func TestValidatingAdmissionPolicyVisibility_PolicyListForbidden_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "validatingadmissionpolicies", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies"}, "", errors.New("forbidden"))
	})

	result, err := check.ValidatingAdmissionPolicyVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the ValidatingAdmissionPolicy list is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
	if !strings.Contains(result.SkipReason, "validatingadmissionpolicy") {
		t.Errorf("SkipReason must name the resource, got %q", result.SkipReason)
	}
	if strings.Contains(result.SkipReason, "does not serve") {
		t.Errorf("a Forbidden must not be conflated with the API-not-served skip reason, got %q", result.SkipReason)
	}
}

// 17. List(ValidatingAdmissionPolicyBinding) Forbidden -> Skip naming the
// resource.
func TestValidatingAdmissionPolicyVisibility_BindingListForbidden_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "validatingadmissionpolicybindings", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings"}, "", errors.New("forbidden"))
	})

	result, err := check.ValidatingAdmissionPolicyVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the ValidatingAdmissionPolicyBinding list is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
	if !strings.Contains(result.SkipReason, "validatingadmissionpolicybinding") {
		t.Errorf("SkipReason must name the resource, got %q", result.SkipReason)
	}
}

// 18. List(ValidatingAdmissionPolicy) returns NotFound (the cluster does
// not serve this v1 API, pre-1.30) -> Skip with a DISTINCT reason from the
// Forbidden case, naming the API rather than implying a fixable
// permission.
func TestValidatingAdmissionPolicyVisibility_PolicyListNotFound_SkippedWithDistinctReason(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "validatingadmissionpolicies", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies"}, "")
	})

	result, err := check.ValidatingAdmissionPolicyVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when ValidatingAdmissionPolicy is not served by this cluster")
	}
	if !strings.Contains(result.SkipReason, "does not serve") {
		t.Errorf("want a distinct 'API not served' skip reason, got %q", result.SkipReason)
	}
}

// 19. a matching object has a non-empty namespaceSelector and the
// Namespace Get is denied -> Skip.
func TestValidatingAdmissionPolicyVisibility_NamespaceGetFailsWithNonEmptySelector_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	mc := podCreateMatchConstraints()
	mc.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}
	policy := vapPolicy("ns-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{MatchConstraints: mc})
	binding := vapBinding("ns-binding", "ns-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})
	client := fake.NewSimpleClientset(policy, binding)
	client.PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies getting Namespaces")
	})

	result, err := check.ValidatingAdmissionPolicyVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the Namespace is not accessible and a matching pair has a non-empty namespaceSelector")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

// 20. Namespace Get fails BUT no matching pair needs it -> the check still
// evaluates (no Skip).
func TestValidatingAdmissionPolicyVisibility_NamespaceGetFailsWithoutSelector_StillEvaluates(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	policy := vapPolicy("deny-policy", admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: podCreateMatchConstraints(),
	})
	binding := vapBinding("deny-binding", "deny-policy", admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})
	client := fake.NewSimpleClientset(policy, binding)
	client.PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies getting Namespaces")
	})

	result, err := check.ValidatingAdmissionPolicyVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if result.Skipped {
		t.Fatalf("no matching pair needs the Namespace object: want Skipped=false, got %+v", result)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != model.SeverityLow {
		t.Fatalf("want one Low finding evaluated without ever reading the Namespace, got %+v", result)
	}
}
