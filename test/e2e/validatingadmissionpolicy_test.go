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

//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// ValidatingAdmissionPolicy/ValidatingAdmissionPolicyBinding are
// cluster-scoped, same hazard already documented in
// admissionwebhook_test.go for webhook configurations: every fixture here
// is pinned to exactly one namespace via matchConstraints.namespaceSelector
// on the well-known kubernetes.io/metadata.name label, and removed via
// t.Cleanup, so it cannot intercept pod creation for other e2e scenarios
// sharing the same cluster/process.

func createVAP(t *testing.T, client kubernetes.Interface, name, namespace string, spec admissionregistrationv1.ValidatingAdmissionPolicySpec) {
	t.Helper()
	if spec.MatchConstraints == nil {
		spec.MatchConstraints = &admissionregistrationv1.MatchResources{}
	}
	spec.MatchConstraints.ResourceRules = []admissionregistrationv1.NamedRuleWithOperations{{
		RuleWithOperations: admissionregistrationv1.RuleWithOperations{
			Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
			Rule: admissionregistrationv1.Rule{
				APIGroups:   []string{""},
				APIVersions: []string{"v1"},
				Resources:   []string{"pods"},
			},
		},
	}}
	spec.MatchConstraints.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{corev1.LabelMetadataName: namespace},
	}
	vap := &admissionregistrationv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
	if _, err := client.AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(context.Background(), vap, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ValidatingAdmissionPolicy %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = client.AdmissionregistrationV1().ValidatingAdmissionPolicies().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
}

func createVAPBinding(t *testing.T, client kubernetes.Interface, name, policyName string, spec admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec) {
	t.Helper()
	spec.PolicyName = policyName
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
	if _, err := client.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(context.Background(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ValidatingAdmissionPolicyBinding %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = client.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
}

// pollUntilPolicyEnforced polls with a lightweight dry-run Pod create until
// policyName is observed actually denying admission, or timeout elapses.
// Verified live on kind in this session: applying a policy+binding and
// immediately creating the triggering workload in the same instant can race
// the apiserver admission plugin's own informer sync, letting a pod slip
// through completely undenied. This closes that gap with a real observed
// signal instead of a guessed fixed sleep, the same principle already
// applied elsewhere in this suite (pollUntilQuotaUsed,
// pollUntilDeploymentConditionReason).
func pollUntilPolicyEnforced(t *testing.T, client kubernetes.Interface, namespace, policyName string, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for {
		probe := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "vap-sync-probe-", Namespace: namespace},
			Spec:       plainPodSpec(),
		}
		_, err := client.CoreV1().Pods(namespace).Create(ctx, probe, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err != nil && strings.Contains(err.Error(), policyName) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ValidatingAdmissionPolicy %q was not observed enforcing within %s (admission-plugin informer sync lag?), last dry-run error: %v", policyName, timeout, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestCheckE2E_ValidatingAdmissionPolicyVisibility_DenyAction_LowFindingAndRealBlock
// reproduces the exact scenario verified manually on kind in this session: a
// policy whose validation always evaluates to false, bound with
// validationActions: [Deny], blocks every pod the ReplicaSet controller
// attempts to create, `kubectl apply` of the Deployment itself succeeds, and
// the only visible signal is a FailedCreate event on the ReplicaSet.
func TestCheckE2E_ValidatingAdmissionPolicyVisibility_DenyAction_LowFindingAndRealBlock(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	policyName := fmt.Sprintf("e2e-vap-deny-%d", time.Now().UnixNano())
	bindingName := policyName + "-binding"
	createVAP(t, admin, policyName, ns, admissionregistrationv1.ValidatingAdmissionPolicySpec{
		Validations: []admissionregistrationv1.Validation{{Expression: "false", Message: "e2e deny"}},
	})
	createVAPBinding(t, admin, bindingName, policyName, admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})
	pollUntilPolicyEnforced(t, admin, ns, policyName, 15*time.Second)

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.ValidatingAdmissionPolicyVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (VAP list is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityLow {
		t.Errorf("severity = %v, want Low", f.Severity)
	}
	if f.Resource.Kind != "ValidatingAdmissionPolicy" || f.Resource.Name != policyName {
		t.Errorf("Resource = %+v, want ValidatingAdmissionPolicy/%s", f.Resource, policyName)
	}

	// Confirm the fact the check is reporting is real, not only that the
	// check's own read of the policy objects matches: no pod for this
	// Deployment is ever created while the deny policy stands, and the
	// ReplicaSet records a FailedCreate event naming this exact policy and
	// binding.
	pods, err := admin.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=victim"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Errorf("want zero pods created while the Deny policy stands, got %d", len(pods.Items))
	}
	waitForFailedCreateEvent(t, admin, ns, 15*time.Second, policyName, bindingName)
}

// waitForFailedCreateEvent polls, rather than reading events once, because
// the controller's own retry-and-record loop can take a beat longer than
// the immediate zero-pods check above.
func waitForFailedCreateEvent(t *testing.T, client kubernetes.Interface, namespace string, timeout time.Duration, mustContain ...string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	var lastEvents []corev1.Event
	for {
		events, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("listing events: %v", err)
		}
		lastEvents = events.Items
		for _, e := range events.Items {
			if e.Reason != "FailedCreate" {
				continue
			}
			allMatch := true
			for _, want := range mustContain {
				if !strings.Contains(e.Message, want) {
					allMatch = false
					break
				}
			}
			if allMatch {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("want a FailedCreate event matching %v within %s, got events: %+v", mustContain, timeout, lastEvents)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// TestCheckE2E_ValidatingAdmissionPolicyVisibility_WarnOnly_NoFindingAndPodsCreated
// proves the sharpest edge of this check's scoping wrong if it ever
// regresses: validationActions without Deny never blocks admission, so the
// identical always-false validation produces zero findings and the rollout
// completes normally.
func TestCheckE2E_ValidatingAdmissionPolicyVisibility_WarnOnly_NoFindingAndPodsCreated(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	policyName := fmt.Sprintf("e2e-vap-warn-%d", time.Now().UnixNano())
	createVAP(t, admin, policyName, ns, admissionregistrationv1.ValidatingAdmissionPolicySpec{
		Validations: []admissionregistrationv1.Validation{{Expression: "false", Message: "e2e warn only"}},
	})
	createVAPBinding(t, admin, policyName+"-binding", policyName, admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Warn},
	})

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.ValidatingAdmissionPolicyVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("validationActions without Deny must never produce a finding, got %+v", res)
	}

	watchAndExpectSuccess(t, admin, ns, d, 2*time.Minute)
}

// TestCheckE2E_ValidatingAdmissionPolicyVisibility_ParamKindNoParamRefFail_MediumFindingAndRealBlock
// reproduces F2's mechanism live: a policy with paramKind set, bound without
// a paramRef, binds params to null; the validation's dereference of
// params.data.foo then raises a CEL runtime error, which failurePolicy=Fail
// turns into the same denial as an explicit false. Verified live on kind in
// this session with the verbatim message captured in the assertion below.
func TestCheckE2E_ValidatingAdmissionPolicyVisibility_ParamKindNoParamRefFail_MediumFindingAndRealBlock(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	failPolicy := admissionregistrationv1.Fail
	policyName := fmt.Sprintf("e2e-vap-paramkind-fail-%d", time.Now().UnixNano())
	bindingName := policyName + "-binding"
	createVAP(t, admin, policyName, ns, admissionregistrationv1.ValidatingAdmissionPolicySpec{
		FailurePolicy: &failPolicy,
		ParamKind:     &admissionregistrationv1.ParamKind{APIVersion: "v1", Kind: "ConfigMap"},
		Validations:   []admissionregistrationv1.Validation{{Expression: "params.data.foo == 'bar'"}},
	})
	createVAPBinding(t, admin, bindingName, policyName, admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})
	pollUntilPolicyEnforced(t, admin, ns, policyName, 15*time.Second)

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.ValidatingAdmissionPolicyVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result, got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium", res.Findings[0].Severity)
	}

	pods, err := admin.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=victim"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Errorf("want zero pods created, got %d", len(pods.Items))
	}
	waitForFailedCreateEvent(t, admin, ns, 15*time.Second, "no such key")
}

// TestCheckE2E_ValidatingAdmissionPolicyVisibility_ParamKindNoParamRefIgnore_LowFindingOnlyAndPodsCreated
// is F2's control: the identical paramKind/no-paramRef configuration with
// failurePolicy=Ignore never denies (a CEL runtime error is dropped under
// Ignore), so the check must report only F1 (Low, the policy is still in
// the deny path via validationActions:[Deny]), never F2 (Medium).
func TestCheckE2E_ValidatingAdmissionPolicyVisibility_ParamKindNoParamRefIgnore_LowFindingOnlyAndPodsCreated(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	ignorePolicy := admissionregistrationv1.Ignore
	policyName := fmt.Sprintf("e2e-vap-paramkind-ignore-%d", time.Now().UnixNano())
	createVAP(t, admin, policyName, ns, admissionregistrationv1.ValidatingAdmissionPolicySpec{
		FailurePolicy: &ignorePolicy,
		ParamKind:     &admissionregistrationv1.ParamKind{APIVersion: "v1", Kind: "ConfigMap"},
		Validations:   []admissionregistrationv1.Validation{{Expression: "params.data.foo == 'bar'"}},
	})
	createVAPBinding(t, admin, policyName+"-binding", policyName, admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.ValidatingAdmissionPolicyVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding (F1 only), got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityLow {
		t.Errorf("severity = %v, want Low: failurePolicy=Ignore means a CEL error never denies, so F2 must not fire", res.Findings[0].Severity)
	}

	watchAndExpectSuccess(t, admin, ns, d, 2*time.Minute)
}

// TestCheckE2E_ValidatingAdmissionPolicyVisibility_NamespaceSelectorExcludesTarget_NoFinding
// proves the check correctly reads namespaceSelector against the real
// Namespace object's labels, mirroring the analogous webhook test: a policy
// scoped to a different namespace must never be reported against this one.
func TestCheckE2E_ValidatingAdmissionPolicyVisibility_NamespaceSelectorExcludesTarget_NoFinding(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	otherNs := newE2ENamespace(t, admin)
	ctx := context.Background()

	policyName := fmt.Sprintf("e2e-vap-otherns-%d", time.Now().UnixNano())
	// Scoped to otherNs, evaluated against ns.
	createVAP(t, admin, policyName, otherNs, admissionregistrationv1.ValidatingAdmissionPolicySpec{
		Validations: []admissionregistrationv1.Validation{{Expression: "false", Message: "e2e deny"}},
	})
	createVAPBinding(t, admin, policyName+"-binding", policyName, admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.ValidatingAdmissionPolicyVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a policy scoped to a different namespace must produce no finding here, got %+v", res)
	}

	watchAndExpectSuccess(t, admin, ns, d, 2*time.Minute)
}

// TestCheckE2E_ValidatingAdmissionPolicyVisibility_ObjectSelectorExcludes_NoFinding
// proves the check correctly reads objectSelector against the real pod
// template's labels: a policy that opts in only pods carrying a label this
// workload's pod template does not have must produce no finding.
func TestCheckE2E_ValidatingAdmissionPolicyVisibility_ObjectSelectorExcludes_NoFinding(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	policyName := fmt.Sprintf("e2e-vap-objsel-%d", time.Now().UnixNano())
	createVAP(t, admin, policyName, ns, admissionregistrationv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionregistrationv1.MatchResources{
			ObjectSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"opt-in": "true"}},
		},
		Validations: []admissionregistrationv1.Validation{{Expression: "false", Message: "e2e deny"}},
	})
	createVAPBinding(t, admin, policyName+"-binding", policyName, admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
		ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
	})

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.ValidatingAdmissionPolicyVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("objectSelector must exclude a pod template without the opt-in label, got %+v", res)
	}

	watchAndExpectSuccess(t, admin, ns, d, 2*time.Minute)
}
