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

// admission webhook configurations are cluster-scoped: unlike every other
// fixture in this package, deleting the ephemeral test namespace does not
// remove them, and an unscoped one would intercept pod creation for the
// entire cluster (including other e2e scenarios running in the same
// process). Every fixture created here is therefore pinned to exactly one
// namespace via namespaceSelector on the well-known
// kubernetes.io/metadata.name label (corev1.LabelMetadataName is auto-applied
// by the API server to every namespace, verified live on kind in this
// session against a namespace with no user-set labels at all), and removed
// via t.Cleanup.

func createValidatingWebhook(t *testing.T, client kubernetes.Interface, name, namespace string, failurePolicy admissionregistrationv1.FailurePolicyType, svcName string) {
	t.Helper()
	ctx := context.Background()
	whc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name:                    name + ".e2e.safe-rollout.example.com",
			AdmissionReviewVersions: []string{"v1"},
			SideEffects:             sideEffectsNonePtr(),
			FailurePolicy:           &failurePolicy,
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{
					Namespace: namespace,
					Name:      svcName,
					Path:      strPtr("/validate"),
				},
			},
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{corev1.LabelMetadataName: namespace},
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{""},
					APIVersions: []string{"v1"},
					Resources:   []string{"pods"},
				},
			}},
		}},
	}
	if _, err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, whc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ValidatingWebhookConfiguration %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
}

func createMutatingWebhook(t *testing.T, client kubernetes.Interface, name, namespace, svcName string) {
	t.Helper()
	ctx := context.Background()
	ignore := admissionregistrationv1.Ignore
	whc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name:                    name + ".e2e.safe-rollout.example.com",
			AdmissionReviewVersions: []string{"v1"},
			SideEffects:             sideEffectsNonePtr(),
			FailurePolicy:           &ignore, // irrelevant to the mutating finding, kept harmless for the shared cluster
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{
					Namespace: namespace,
					Name:      svcName,
					Path:      strPtr("/mutate"),
				},
			},
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{corev1.LabelMetadataName: namespace},
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{""},
					APIVersions: []string{"v1"},
					Resources:   []string{"pods"},
				},
			}},
		}},
	}
	if _, err := client.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, whc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating MutatingWebhookConfiguration %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = client.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
}

func sideEffectsNonePtr() *admissionregistrationv1.SideEffectClass {
	v := admissionregistrationv1.SideEffectClassNone
	return &v
}

func strPtr(s string) *string { return &s }

func plainPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
}

// TestCheckE2E_AdmissionWebhookVisibility_FailurePolicyFailMissingBackend_High
// reproduces, through the check package rather than a hand run kubectl
// session, the exact scenario verified manually on kind in this session: a
// failurePolicy: Fail webhook whose backend Service does not exist blocks
// every pod creation the ReplicaSet controller attempts, and the check must
// report that as a verified High finding, not a heuristic.
func TestCheckE2E_AdmissionWebhookVisibility_FailurePolicyFailMissingBackend_High(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	whName := fmt.Sprintf("e2e-wh-fail-missing-%d", time.Now().UnixNano())
	createValidatingWebhook(t, admin, whName, ns, admissionregistrationv1.Fail, "does-not-exist")

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.AdmissionWebhookVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (webhook configuration list is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.Resource.Kind != "ValidatingWebhookConfiguration" || f.Resource.Name != whName {
		t.Errorf("Resource = %+v, want ValidatingWebhookConfiguration/%s", f.Resource, whName)
	}

	// Confirm the fact the check is reporting is real, not only that the
	// check's own read of the config objects matches: no pod for this
	// Deployment is ever created while the webhook stands.
	pods, err := admin.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=victim"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Errorf("want zero pods created while the failurePolicy:Fail webhook's backend Service is missing, got %d", len(pods.Items))
	}
}

// TestCheckE2E_AdmissionWebhookVisibility_FailurePolicyIgnore_NoFinding
// proves the sharpest edge of the check's justification wrong if it ever
// regresses: the identical unreachable backend produces no finding at all
// once failurePolicy is Ignore, because failurePolicy governs only call
// errors, never an explicit denial from a healthy webhook. Verified live on
// kind in this session: the same Deployment rolled out successfully against
// the same missing backend once failurePolicy flipped to Ignore.
func TestCheckE2E_AdmissionWebhookVisibility_FailurePolicyIgnore_NoFinding(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	whName := fmt.Sprintf("e2e-wh-ignore-missing-%d", time.Now().UnixNano())
	createValidatingWebhook(t, admin, whName, ns, admissionregistrationv1.Ignore, "does-not-exist")

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.AdmissionWebhookVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("failurePolicy:Ignore must never produce a finding regardless of backend reachability, got %+v", res)
	}
}

// TestCheckE2E_AdmissionWebhookVisibility_NamespaceSelectorExcludesTarget_NoFinding
// proves the check correctly reads namespaceSelector against the real
// Namespace object's labels: a webhook scoped to a different namespace must
// never be reported against this one, matching the live kind reproduction
// in this session.
func TestCheckE2E_AdmissionWebhookVisibility_NamespaceSelectorExcludesTarget_NoFinding(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	otherNs := newE2ENamespace(t, admin)
	ctx := context.Background()

	whName := fmt.Sprintf("e2e-wh-other-ns-%d", time.Now().UnixNano())
	// Scoped to otherNs, evaluated against ns.
	createValidatingWebhook(t, admin, whName, otherNs, admissionregistrationv1.Fail, "does-not-exist")

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.AdmissionWebhookVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a webhook scoped to a different namespace must produce no finding here, got %+v", res)
	}
}

// TestCheckE2E_AdmissionWebhookVisibility_MutatingWebhook_Low proves the
// purely informational mutating-webhook finding fires independent of
// failurePolicy or backend reachability, against a real cluster read of a
// live MutatingWebhookConfiguration.
func TestCheckE2E_AdmissionWebhookVisibility_MutatingWebhook_Low(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	whName := fmt.Sprintf("e2e-mwh-%d", time.Now().UnixNano())
	createMutatingWebhook(t, admin, whName, ns, "does-not-exist")

	d := deployWorkload(t, admin, ns, "victim", 1, plainPodSpec(), nil)

	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(d), Client: admin}
	res, err := check.AdmissionWebhookVisibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result, got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityLow {
		t.Errorf("severity = %v, want Low", f.Severity)
	}
	if f.Resource.Kind != "MutatingWebhookConfiguration" || f.Resource.Name != whName {
		t.Errorf("Resource = %+v, want MutatingWebhookConfiguration/%s", f.Resource, whName)
	}
}
