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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// The project promises everywhere that a check which cannot read a resource
// degrades to check.Skip with a reason, rather than failing the whole run
// (rules/go.md). Until this scenario existed that promise was only ever
// exercised against fake-clientset reactors returning synthetic errors, which
// prove that the code handles *an* error and nothing about what a real API
// server returns to a real restricted ServiceAccount.
//
// The distinction matters because the two differ: the API server answers a
// forbidden read with a 403 carrying a message naming the missing verb and
// resource, and client-go surfaces it through a path that a hand-made
// errors.New in a reactor never touches.
func TestCheckE2E_RestrictedRBAC_SkipsInsteadOfFailing(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	// Deliberately narrow: enough to read the workload and its pods, and
	// nothing else. poddisruptionbudgets and resourcequotas are omitted so
	// the checks that need them have to degrade.
	const saName = "restricted-reader"
	if _, err := admin.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ServiceAccount: %v", err)
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "restricted-reader", Namespace: ns},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods", "events", "serviceaccounts"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"apps"}, Resources: []string{"deployments", "replicasets"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	if _, err := admin.RbacV1().Roles(ns).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating Role: %v", err)
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "restricted-reader", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "restricted-reader"},
	}
	if _, err := admin.RbacV1().RoleBindings(ns).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating RoleBinding: %v", err)
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
	d := deployWorkload(t, admin, ns, "restricted", 1, podSpec, nil)

	// A PodDisruptionBudget exists and would normally be read by
	// pdb-consistency. The restricted account cannot see it, which is the
	// point: the check must say so rather than report that nothing is wrong.
	minAvailable := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "restricted", Namespace: ns},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "restricted"}},
		},
	}
	if _, err := admin.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, pdb, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating PodDisruptionBudget: %v", err)
	}

	restricted := newServiceAccountClient(t, ns, saName)
	live, err := restricted.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the restricted account must be able to read its own workload, got: %v", err)
	}

	target := check.Target{
		Namespace: ns,
		Workload:  workload.FromDeployment(live),
		Client:    restricted,
	}

	var skipped, evaluated []string
	for _, c := range []check.Check{
		check.PDBConsistency{},
		check.QuotaHeadroom{},
		check.ProbeSanity{},
		check.ResourceLimits{},
		check.ImagePullSecrets{},
	} {
		res, err := c.Run(ctx, target)
		if err != nil {
			t.Fatalf("check %q returned an error under restricted RBAC; the contract is to degrade with Skip, not to fail the run: %v", c.ID(), err)
		}
		if res.Skipped {
			if strings.TrimSpace(res.SkipReason) == "" {
				t.Errorf("check %q skipped without a reason: a silent skip is indistinguishable from a clean result", c.ID())
			}
			skipped = append(skipped, c.ID())
			continue
		}
		evaluated = append(evaluated, c.ID())
	}

	// Degradation must be partial. A run where everything skips would satisfy
	// "no error" while being useless, so assert that the checks needing only
	// the workload itself still did their job.
	if len(evaluated) == 0 {
		t.Fatalf("every check skipped: checks reading only the pod template must still evaluate (skipped=%v)", skipped)
	}
	for _, want := range []string{check.ProbeSanityCheckID, check.ResourceLimitsCheckID} {
		if !contains(evaluated, want) {
			t.Errorf("check %q needs nothing beyond the workload and must not skip (evaluated=%v, skipped=%v)", want, evaluated, skipped)
		}
	}
	if len(skipped) == 0 {
		t.Errorf("no check skipped: the Role deliberately withholds poddisruptionbudgets and resourcequotas, so at least one must degrade (evaluated=%v)", evaluated)
	}
	t.Logf("under restricted RBAC: evaluated=%v skipped=%v", evaluated, skipped)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
