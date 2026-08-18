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

// Package e2e_test contains the end-to-end `watch` scenarios against a
// real kind cluster (project quality criterion: one scenario for each
// classified cause). It is isolated from the rest of the suite with the
// "e2e" build tag so `go test ./...` (including CI) does not require a
// cluster: run it explicitly with `make test-e2e`, which expects an
// already running kind cluster named "safe-rollout" (see README).
package e2e_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// defaultE2EContext is the conventional kubeconfig context expected in
// this repository (`kind create cluster --name safe-rollout`). It can be
// overridden with E2E_CONTEXT when using a different name.
const defaultE2EContext = "kind-safe-rollout"

// newE2EClient builds a real Kubernetes client from the user's
// kubeconfig, using the kind cluster context. It fails the test with an
// explicit message (not a silent skip) if the context does not exist: an
// e2e scenario that does not run because the cluster is missing must be
// visible, not mistaken for a success.
func newE2EClient(t *testing.T) kubernetes.Interface {
	t.Helper()
	ctxName := os.Getenv("E2E_CONTEXT")
	if ctxName == "" {
		ctxName = defaultE2EContext
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: ctxName}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		t.Fatalf("configuration for context %q is unavailable: %v (a kind cluster is required: kind create cluster --name safe-rollout)", ctxName, err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("creating Kubernetes client: %v", err)
	}
	return client
}

// newE2ENamespace creates a disposable namespace to isolate the scenario
// and deletes it at the end of the test, so scenarios can run in
// parallel or sequentially without interfering with one another.
func newE2ENamespace(t *testing.T, client kubernetes.Interface) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("e2e-%d-%d", time.Now().UnixNano(), rand.Intn(100000))
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating namespace %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = client.CoreV1().Namespaces().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	return name
}

// deployWorkload creates a single-container Deployment with matching
// selector labels in Spec.Selector and Template, so each scenario only
// needs to describe the PodSpec that produces the cause to classify.
func deployWorkload(t *testing.T, client kubernetes.Interface, namespace, name string, replicas int32, podSpec corev1.PodSpec, mutate func(*appsv1.Deployment)) *appsv1.Deployment {
	t.Helper()
	labels := map[string]string{"app": name}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
	if mutate != nil {
		mutate(d)
	}
	created, err := client.AppsV1().Deployments(namespace).Create(context.Background(), d, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating Deployment %s/%s: %v", namespace, name, err)
	}
	return created
}

// watchAndExpectCause runs diagnose.Watch against the real cluster and
// verifies that at least one resulting Finding has the expected CauseID:
// this is the same function that powers `kubectl safe-rollout watch`,
// not a test shortcut.
func watchAndExpectCause(t *testing.T, client kubernetes.Interface, namespace string, d *appsv1.Deployment, causeID diagnose.CauseID, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome, err := diagnose.Watch(ctx, diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Watch returned an unexpected error: %v", err)
	}
	if outcome.Succeeded {
		t.Fatalf("Watch reported success, expected failure %q", causeID)
	}
	for _, res := range outcome.Results {
		for _, f := range res.Findings {
			if f.CheckID == string(causeID) {
				return
			}
		}
	}
	t.Fatalf("cause %q not found among Results: %+v", causeID, outcome.Results)
}

// watchAndExpectSuccess runs diagnose.Watch against the real cluster and
// verifies that the rollout completes without any Finding. This protects
// slow-starting applications from readiness false positives.
func watchAndExpectSuccess(t *testing.T, client kubernetes.Interface, namespace string, d *appsv1.Deployment, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome, err := diagnose.Watch(ctx, diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Watch returned an unexpected error: %v", err)
	}
	findings := diagnose.AllFindings(outcome.Results)
	if len(findings) != 0 {
		t.Fatalf("Watch reported findings for a successful rollout: %+v", findings)
	}
	if !outcome.Succeeded {
		t.Fatalf("Watch did not report success: %+v", outcome)
	}
}
