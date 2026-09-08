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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"

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
			// Gives config-references-exist something to actually Get
			// (and be denied): without a reference in the pod template it
			// short-circuits before touching the API at all, and this
			// scenario would never exercise its degrade path.
			Env: []corev1.EnvVar{{
				Name: "SOME_VALUE",
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "restricted-config"},
						Key:                  "value",
					},
				},
			}},
		}},
		// Gives pvc-exists something to actually Get (and be denied) too.
		Volumes: []corev1.Volume{{
			Name:         "data",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "restricted-data"}},
		}},
		// Gives priorityclass-exists something to actually Get (and be
		// denied) too: without this, its early "unset" return would never
		// touch the API and the scenario would prove nothing about its
		// degrade path.
		PriorityClassName: "restricted-priority",
		// Gives scheduling-constraints-feasibility something to actually
		// evaluate: without a real topologySpreadConstraint/anti-affinity
		// term it short-circuits before ever calling Nodes().List(), and
		// this scenario would prove nothing about its degrade path (the
		// restricted Role above grants nothing on the cluster-scoped nodes
		// resource at all).
		TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "restricted"}},
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
		check.PDBEvictionBlocked{},
		check.QuotaHeadroom{},
		check.HPAQuotaHeadroom{},
		check.LimitRangeFeasibility{},
		check.SelectorOverlap{},
		check.ServiceAccountExists{},
		check.PriorityClassExists{},
		check.ServiceRouting{},
		check.IngressRouting{},
		check.IngressClassExists{},
		check.ConfigReferencesExist{},
		check.PVCExists{},
		check.NetworkPolicyIngress{},
		check.SchedulingConstraintsFeasibility{},
		check.AdmissionWebhookVisibility{},
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
	for _, want := range []string{check.ProbeSanityCheckID, check.ResourceLimitsCheckID, check.ServiceAccountExistsCheckID} {
		if !contains(evaluated, want) {
			t.Errorf("check %q needs nothing beyond a resource the Role grants and must not skip (evaluated=%v, skipped=%v)", want, evaluated, skipped)
		}
	}
	// service-routing, ingress-routing and ingressclass-exists all need to
	// list services (not granted) — none of the three ever gets far enough
	// to touch Ingresses or IngressClasses in this scenario, since no
	// Ingress or Service fixture exists at all here and the ServiceList
	// call is denied first; config-references-exist needs to get the
	// ConfigMap the pod template references (also not granted); pvc-exists
	// needs to get the PersistentVolumeClaim the pod template's volume
	// references (also not granted); network-policy-ingress needs to list
	// networkpolicies (also not granted); hpa-quota-headroom needs to list
	// horizontalpodautoscalers (also not granted); priorityclass-exists
	// needs to get the PriorityClass the pod template names (also not
	// granted — PriorityClass is cluster-scoped, and the Role above grants
	// nothing in the scheduling.k8s.io API group at all);
	// scheduling-constraints-feasibility needs to list the cluster-scoped
	// nodes resource (also not granted — a namespaced Role can never grant
	// it in the first place). selector-overlap needs both a Deployment list
	// (granted) AND a StatefulSet list (not granted — the Role above grants
	// nothing on statefulsets at all): either list failing must Skip the
	// whole check by design, so it lands here even though its Deployment
	// list alone would have succeeded. admission-webhook-visibility needs to
	// list the cluster-scoped ValidatingWebhookConfiguration/
	// MutatingWebhookConfiguration resources (also not granted — a
	// namespaced Role can never grant anything in the
	// admissionregistration.k8s.io API group either): unlike
	// config-references-exist/pvc-exists, this needs no reference in the pod
	// template to exercise its degrade path, since the very first read it
	// performs is already cluster-scoped and denied.
	// pdb-eviction-blocked lists PodDisruptionBudgets before any other
	// return except the replicas==0 guard (see internal/check/pdbeviction.go):
	// the Deployment target above has 1 replica, so it always reaches, and
	// is denied by, that List call, exercising its real degrade path with no
	// dedicated fixture needed.
	// limitrange-feasibility lists LimitRanges (also not granted) as its
	// very first operation, unconditionally, before it ever inspects the
	// pod template: no dedicated fixture is needed either, the same as
	// pdb-eviction-blocked above.
	for _, want := range []string{check.ServiceRoutingCheckID, check.IngressRoutingCheckID, check.IngressClassExistsCheckID, check.ConfigReferencesExistCheckID, check.PVCExistsCheckID, check.NetworkPolicyIngressCheckID, check.HPAQuotaHeadroomCheckID, check.PriorityClassExistsCheckID, check.SchedulingConstraintsFeasibilityCheckID, check.SelectorOverlapCheckID, check.AdmissionWebhookVisibilityCheckID, check.PDBEvictionBlockedCheckID, check.LimitRangeFeasibilityCheckID} {
		if !contains(skipped, want) {
			t.Errorf("check %q needs a resource the Role withholds and must skip, not fail or silently report clean (evaluated=%v, skipped=%v)", want, evaluated, skipped)
		}
	}
	if len(skipped) == 0 {
		t.Errorf("no check skipped: the Role deliberately withholds poddisruptionbudgets, resourcequotas, services, ingresses, and configmaps, so at least one must degrade (evaluated=%v)", evaluated)
	}
	t.Logf("under restricted RBAC: evaluated=%v skipped=%v", evaluated, skipped)

	// storageclass-exists cannot be exercised through the Deployment-backed
	// target above at all: Workload.VolumeClaimTemplates() always returns nil
	// for a Deployment (the field does not exist on appsv1.DeploymentSpec),
	// so the check short-circuits with zero API calls no matter what the Role
	// grants. It needs a StatefulSet with a real volumeClaimTemplate instead
	// — a second, separate target, not a variant of the loop above.
	sts := deployStatefulSet(t, admin, ns, "restricted-sts", 1, corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}, func(s *appsv1.StatefulSet) {
		storageClass := "restricted-storage-class"
		s.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data"},
			Spec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: &storageClass,
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}}
	})
	// check.Target wraps an already-fetched object (Workload), the same way
	// the Deployment target above wraps a live Get result — but that Get
	// itself needs no permission check here: the point of this scenario is
	// storageclass-exists's own StorageClass Get, which no namespaced Role
	// can ever grant regardless of what it says about statefulsets.
	stsTarget := check.Target{
		Namespace: ns,
		Workload:  workload.FromStatefulSet(sts),
		Client:    restricted,
	}
	stsRes, err := check.StorageClassExists{}.Run(ctx, stsTarget)
	if err != nil {
		t.Fatalf("storageclass-exists returned an error under restricted RBAC; the contract is to degrade with Skip, not to fail the run: %v", err)
	}
	if !stsRes.Skipped {
		t.Errorf("storageclass-exists needs a cluster-scoped Get on StorageClass, which no namespaced Role can ever grant, and must skip (got Skipped=false, findings=%+v)", stsRes.Findings)
	} else if strings.TrimSpace(stsRes.SkipReason) == "" {
		t.Errorf("storageclass-exists skipped without a reason: a silent skip is indistinguishable from a clean result")
	}

	// pdb-daemonset-scale cannot be exercised through the Deployment-backed
	// target above at all, and for a reason different from
	// storageclass-exists: it does not short-circuit before touching the
	// API because of a nil/empty field, it short-circuits on
	// Workload.Kind() != "DaemonSet" and returns an evaluated, non-skipped,
	// zero-finding Result without ever calling PodDisruptionBudgets().List
	// — correct behavior for a Deployment target, but not a skip. Adding it
	// to the main loop's "must skip" list above would assert something
	// false about what the check actually does there. A third, separate
	// target exercises its real degrade path: a DaemonSet with a
	// maxUnavailable PodDisruptionBudget selecting it, read by the same
	// restricted client the Role above denies poddisruptionbudgets to.
	dsForPDB := deployDaemonSet(t, admin, ns, "restricted-ds", corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}, nil)
	// pdb-daemonset-scale reads Workload.Replicas() (desiredNumberScheduled)
	// before ever listing PodDisruptionBudgets: a DaemonSet object read
	// immediately after creation still has a zero status, which would make
	// the check return early with the same "evaluated but never touched the
	// API" outcome this fixture is specifically trying to avoid. Wait for
	// the controller to populate it first, using the admin client (the
	// point of this scenario is the PDB List call being denied, not the
	// DaemonSet Get, and no namespaced Role could grant daemonsets access
	// anyway since the Role above never mentions them).
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true, func(pollCtx context.Context) (bool, error) {
		live, err := admin.AppsV1().DaemonSets(ns).Get(pollCtx, dsForPDB.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		dsForPDB = live
		return live.Status.DesiredNumberScheduled > 0, nil
	}); err != nil {
		t.Fatalf("DaemonSet %s/%s never reported a nonzero desiredNumberScheduled: %v", ns, dsForPDB.Name, err)
	}

	dsMaxUnavailable := intstr.FromInt32(1)
	dsPDB := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "restricted-ds", Namespace: ns},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &dsMaxUnavailable,
			Selector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "restricted-ds"}},
		},
	}
	if _, err := admin.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, dsPDB, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating PodDisruptionBudget: %v", err)
	}

	dsTarget := check.Target{
		Namespace: ns,
		Workload:  workload.FromDaemonSet(dsForPDB),
		Client:    restricted,
	}
	dsRes, err := check.PDBDaemonsetScale{}.Run(ctx, dsTarget)
	if err != nil {
		t.Fatalf("pdb-daemonset-scale returned an error under restricted RBAC; the contract is to degrade with Skip, not to fail the run: %v", err)
	}
	if !dsRes.Skipped {
		t.Errorf("pdb-daemonset-scale needs to list PodDisruptionBudgets, which the Role withholds, and must skip (got Skipped=false, findings=%+v)", dsRes.Findings)
	} else if strings.TrimSpace(dsRes.SkipReason) == "" {
		t.Errorf("pdb-daemonset-scale skipped without a reason: a silent skip is indistinguishable from a clean result")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
