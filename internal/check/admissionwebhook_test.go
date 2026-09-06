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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func failurePolicyPtr(v admissionregistrationv1.FailurePolicyType) *admissionregistrationv1.FailurePolicyType {
	return &v
}
func scopePtr(v admissionregistrationv1.ScopeType) *admissionregistrationv1.ScopeType { return &v }

func podCreateRule() admissionregistrationv1.RuleWithOperations {
	return admissionregistrationv1.RuleWithOperations{
		Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
		Rule: admissionregistrationv1.Rule{
			APIGroups:   []string{""},
			APIVersions: []string{"v1"},
			Resources:   []string{"pods"},
		},
	}
}

func validatingWebhookConfig(name string, webhooks ...admissionregistrationv1.ValidatingWebhook) *admissionregistrationv1.ValidatingWebhookConfiguration {
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks:   webhooks,
	}
}

func mutatingWebhookConfig(name string, webhooks ...admissionregistrationv1.MutatingWebhook) *admissionregistrationv1.MutatingWebhookConfiguration {
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks:   webhooks,
	}
}

func serviceClientConfig(namespace, name string) admissionregistrationv1.WebhookClientConfig {
	return admissionregistrationv1.WebhookClientConfig{
		Service: &admissionregistrationv1.ServiceReference{Namespace: namespace, Name: name},
	}
}

func runAdmissionWebhookVisibilityCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.AdmissionWebhookVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

// 1. matching pods/CREATE, failurePolicy Fail, backend Service NotFound -> High.
func TestAdmissionWebhookVisibility_FailBackendNotFound_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("fail-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "fail.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:         []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:  serviceClientConfig(testNamespace, "does-not-exist"),
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.AdmissionWebhookVisibilityCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "does-not-exist") || !strings.Contains(f.Cause, "fail.example.com") {
		t.Errorf("cause must name the webhook and the missing Service, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
	if f.Resource.Kind != "ValidatingWebhookConfiguration" || f.Resource.Name != "fail-config" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

// 2. same but Service exists with zero EndpointSlice ready -> Medium.
func TestAdmissionWebhookVisibility_FailBackendZeroReady_MediumFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("fail-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "fail.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:         []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:  serviceClientConfig(testNamespace, "webhook-backend"),
	})
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "webhook-backend", Namespace: testNamespace}}

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg, svc)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity=%v, want Medium: backend exists but is not ready", f.Severity)
	}
}

// 3. mutating webhook matches pods/CREATE -> Low, independent of failurePolicy/backend.
func TestAdmissionWebhookVisibility_MutatingMatches_LowFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := mutatingWebhookConfig("mutate-config", admissionregistrationv1.MutatingWebhook{
		Name:         "mutate.example.com",
		Rules:        []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig: serviceClientConfig(testNamespace, "does-not-exist"),
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if f.Severity != model.SeverityLow {
		t.Errorf("severity=%v, want Low", f.Severity)
	}
	if f.Resource.Kind != "MutatingWebhookConfiguration" || f.Resource.Name != "mutate-config" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

// 4. validating webhook matches, failurePolicy Fail, backend resolves with ready endpoints -> Low.
func TestAdmissionWebhookVisibility_FailBackendReady_LowFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("fail-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "fail.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:         []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:  serviceClientConfig(testNamespace, "webhook-backend"),
	})
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "webhook-backend", Namespace: testNamespace}}
	ep := readyEndpointSlice("webhook-backend", 1)

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg, svc, ep)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	if result.Findings[0].Severity != model.SeverityLow {
		t.Errorf("severity=%v, want Low", result.Findings[0].Severity)
	}
}

// 5. validating webhook matches, failurePolicy Ignore, backend NotFound -> no finding.
func TestAdmissionWebhookVisibility_IgnoreBackendNotFound_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("ignore-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "ignore.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Ignore),
		Rules:         []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:  serviceClientConfig(testNamespace, "does-not-exist"),
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("failurePolicy Ignore never blocks on a call error: want empty result, got %+v", result)
	}
}

// 6. no webhook configuration in the cluster -> no finding.
func TestAdmissionWebhookVisibility_NoWebhooks_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})

	result := runAdmissionWebhookVisibilityCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no webhook configurations at all: want empty result, got %+v", result)
	}
}

// 7. rule does not cover pods (e.g. only secrets) -> no finding.
func TestAdmissionWebhookVisibility_RuleCoversOnlySecrets_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("secret-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "secrets.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules: []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
			Rule:       admissionregistrationv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"secrets"}},
		}},
		ClientConfig: serviceClientConfig(testNamespace, "does-not-exist"),
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("rule does not cover pods: want empty result, got %+v", result)
	}
}

// 8. rule covers only UPDATE, not CREATE/* -> no finding.
func TestAdmissionWebhookVisibility_RuleCoversOnlyUpdate_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("update-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "update.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules: []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Update},
			Rule:       admissionregistrationv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}},
		}},
		ClientConfig: serviceClientConfig(testNamespace, "does-not-exist"),
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("rule covers only UPDATE: want empty result, got %+v", result)
	}
}

// 9. scope: Cluster -> no finding (Pod is always namespaced).
func TestAdmissionWebhookVisibility_ScopeCluster_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	rule := podCreateRule()
	rule.Scope = scopePtr(admissionregistrationv1.ClusterScope)
	cfg := validatingWebhookConfig("cluster-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "cluster.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:         []admissionregistrationv1.RuleWithOperations{rule},
		ClientConfig:  serviceClientConfig(testNamespace, "does-not-exist"),
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("scope Cluster never matches a Pod: want empty result, got %+v", result)
	}
}

// 10. namespaceSelector excludes the target namespace -> no finding.
func TestAdmissionWebhookVisibility_NamespaceSelectorExcludes_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("ns-config", admissionregistrationv1.ValidatingWebhook{
		Name:              "ns.example.com",
		FailurePolicy:     failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:             []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:      serviceClientConfig(testNamespace, "does-not-exist"),
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
	})
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace, Labels: map[string]string{"env": "staging"}}}

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg, ns)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("namespaceSelector excludes the target namespace: want empty result, got %+v", result)
	}
}

// 11. objectSelector does not match the target's PodLabels -> no finding.
func TestAdmissionWebhookVisibility_ObjectSelectorExcludes_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("obj-config", admissionregistrationv1.ValidatingWebhook{
		Name:           "obj.example.com",
		FailurePolicy:  failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:          []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:   serviceClientConfig(testNamespace, "does-not-exist"),
		ObjectSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"opt-in": "true"}},
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("objectSelector does not match the pod template's labels: want empty result, got %+v", result)
	}
}

// 12. matchConditions present -> excluded from conclusions, no finding.
func TestAdmissionWebhookVisibility_MatchConditionsPresent_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("cel-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "cel.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:         []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:  serviceClientConfig(testNamespace, "does-not-exist"),
		MatchConditions: []admissionregistrationv1.MatchCondition{
			{Name: "only-prod", Expression: "object.metadata.namespace == 'prod'"},
		},
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("a webhook with matchConditions must be excluded from every conclusion: want empty result, got %+v", result)
	}
}

// 13. all optional fields nil (failurePolicy/matchPolicy/timeoutSeconds/scope):
// documented defaults must be applied as if the apiserver had defaulted them,
// since client-go/kubernetes/fake performs no defaulting. Must still produce High.
func TestAdmissionWebhookVisibility_AllOptionalFieldsNil_DefaultsApplied_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("defaults-config", admissionregistrationv1.ValidatingWebhook{
		Name: "defaults.example.com",
		Rules: []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
			Rule:       admissionregistrationv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}},
		}},
		ClientConfig: serviceClientConfig(testNamespace, "does-not-exist"),
		// FailurePolicy, MatchPolicy, TimeoutSeconds, and Rules[0].Scope all
		// left nil deliberately.
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("nil optional fields must behave like their documented defaults (failurePolicy=Fail, scope=*): want one finding, got %+v", result)
	}
	if result.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity=%v, want High", result.Findings[0].Severity)
	}
}

// 14. rule with total wildcards (apiGroups:["*"], resources:["*/*"] or
// ["pods"], operations:["*"]) -> matches.
func TestAdmissionWebhookVisibility_WildcardRule_Matches(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("wildcard-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "wildcard.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules: []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.OperationAll},
			Rule:       admissionregistrationv1.Rule{APIGroups: []string{"*"}, APIVersions: []string{"*"}, Resources: []string{"*/*"}},
		}},
		ClientConfig: serviceClientConfig(testNamespace, "does-not-exist"),
	})

	result := runAdmissionWebhookVisibilityCheck(t, d, cfg)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("a totally wildcarded rule must match pods/CREATE: want one finding, got %+v", result)
	}
}

// 15. List ValidatingWebhookConfigurations or MutatingWebhookConfigurations
// fails -> Skip the entire check.
func TestAdmissionWebhookVisibility_ValidatingListFails_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "validatingwebhookconfigurations", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing ValidatingWebhookConfigurations")
	})

	result, err := check.AdmissionWebhookVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the ValidatingWebhookConfiguration list is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

func TestAdmissionWebhookVisibility_MutatingListFails_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "mutatingwebhookconfigurations", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing MutatingWebhookConfigurations")
	})

	result, err := check.AdmissionWebhookVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the MutatingWebhookConfiguration list is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

// 16. Namespace Get fails AND at least one matching webhook has a non-empty
// namespaceSelector -> Skip.
func TestAdmissionWebhookVisibility_NamespaceGetFailsWithNonEmptySelector_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("ns-config", admissionregistrationv1.ValidatingWebhook{
		Name:              "ns.example.com",
		FailurePolicy:     failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:             []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:      serviceClientConfig(testNamespace, "does-not-exist"),
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
	})
	client := fake.NewSimpleClientset(cfg)
	client.PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies getting Namespaces")
	})

	result, err := check.AdmissionWebhookVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the Namespace is not accessible and a matching webhook has a non-empty namespaceSelector")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

// 17. Namespace Get fails BUT every matching webhook has an empty/nil
// namespaceSelector -> the check still evaluates (no Skip).
func TestAdmissionWebhookVisibility_NamespaceGetFailsWithEmptySelectors_StillEvaluates(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	cfg := validatingWebhookConfig("fail-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "fail.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:         []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:  serviceClientConfig(testNamespace, "does-not-exist"),
	})
	client := fake.NewSimpleClientset(cfg)
	client.PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies getting Namespaces")
	})

	result, err := check.AdmissionWebhookVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if result.Skipped {
		t.Fatalf("no matching webhook needs the Namespace object: want Skipped=false, got %+v", result)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != model.SeverityHigh {
		t.Fatalf("want one High finding evaluated without ever reading the Namespace, got %+v", result)
	}
}

// 18. Get/List on the backend Service/EndpointSlice fails for one webhook ->
// that specific finding is dropped or degraded, but the check does not Skip
// entirely; other, independent findings remain valid.
func TestAdmissionWebhookVisibility_BackendServiceUnreadable_DropsThatFindingOnly(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	unreadable := validatingWebhookConfig("unreadable-config", admissionregistrationv1.ValidatingWebhook{
		Name:          "unreadable.example.com",
		FailurePolicy: failurePolicyPtr(admissionregistrationv1.Fail),
		Rules:         []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig:  serviceClientConfig(testNamespace, "unreadable-backend"),
	})
	mutating := mutatingWebhookConfig("mutate-config", admissionregistrationv1.MutatingWebhook{
		Name:         "mutate.example.com",
		Rules:        []admissionregistrationv1.RuleWithOperations{podCreateRule()},
		ClientConfig: serviceClientConfig(testNamespace, "does-not-exist"),
	})
	client := fake.NewSimpleClientset(unreadable, mutating)
	client.PrependReactor("get", "services", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction := action.(k8stesting.GetAction)
		if getAction.GetName() == "unreadable-backend" {
			return true, nil, errors.New("forbidden: RBAC denies getting this Service")
		}
		return false, nil, nil
	})

	result, err := check.AdmissionWebhookVisibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade, not fail the whole check: %v", err)
	}
	if result.Skipped {
		t.Fatalf("an unreadable backend Service for one webhook must not Skip the entire check: got %+v", result)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("want exactly one finding (the independent mutating webhook), the validating one's finding dropped: got %+v", result)
	}
	if result.Findings[0].Resource.Kind != "MutatingWebhookConfiguration" {
		t.Errorf("surviving finding must be the mutating webhook's, got %+v", result.Findings[0])
	}
}
