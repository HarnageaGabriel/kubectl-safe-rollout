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
	"math/rand"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// newE2ENamespaceLabeled is newE2ENamespace with caller-supplied labels, for
// the pod-security.kubernetes.io/* labels this check keys on.
func newE2ENamespaceLabeled(t *testing.T, client kubernetes.Interface, labels map[string]string) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("e2e-psa-%d-%d", time.Now().UnixNano(), rand.Intn(100000))
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if _, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating namespace %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = client.CoreV1().Namespaces().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	return name
}

func restrictedCompliantPodSpec() corev1.PodSpec {
	nonRoot := true
	noEsc := false
	uid := int64(1000)
	return corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   &nonRoot,
			RunAsUser:      &uid,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &noEsc,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
		}},
	}
}

func plainRootPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
}

// TestCheckE2E_PodSecurityAdmission_RestrictedEnforce_ViolatingDeployment_High
// is the load-bearing verification: a namespace enforcing restricted plus a
// non-conformant Deployment. It confirms all three facts the check's doc
// comment claims: the Deployment apply succeeds, zero pods are ever created,
// a FailedCreate event carries the PodSecurity rejection, and the check
// reports it as High before any of that happens.
func TestCheckE2E_PodSecurityAdmission_RestrictedEnforce_ViolatingDeployment_High(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespaceLabeled(t, client, map[string]string{"pod-security.kubernetes.io/enforce": "restricted"})
	ctx := context.Background()

	d := deployWorkload(t, client, ns, "bad", 2, plainRootPodSpec(), nil)

	// Zero pods, ever, and a FailedCreate event citing PodSecurity: the
	// ReplicaSet controller's pod creation is denied even though the
	// Deployment object was accepted. Poll rather than sleep a fixed
	// interval: the controller's first create attempt and the resulting
	// event are asynchronous.
	deadline := time.Now().Add(45 * time.Second)
	sawRejection := false
	for time.Now().Before(deadline) {
		pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=bad"})
		if err != nil {
			t.Fatalf("listing pods: %v", err)
		}
		if len(pods.Items) != 0 {
			t.Fatalf("want zero pods (PodSecurity denies creation under enforce: restricted), got %d", len(pods.Items))
		}
		events, err := client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("listing events: %v", err)
		}
		for _, ev := range events.Items {
			if ev.Reason == "FailedCreate" && strings.Contains(ev.Message, "violates PodSecurity") {
				sawRejection = true
				break
			}
		}
		if sawRejection {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if !sawRejection {
		t.Errorf("want a FailedCreate event citing the PodSecurity violation within the deadline")
	}

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.PodSecurityAdmission{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (namespace Get is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// TestCheckE2E_PodSecurityAdmission_RestrictedEnforce_CompliantDeployment_NoFinding
// proves the positive path: a fully restricted-compliant template under
// enforce: restricted rolls out and produces no finding.
func TestCheckE2E_PodSecurityAdmission_RestrictedEnforce_CompliantDeployment_NoFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespaceLabeled(t, client, map[string]string{"pod-security.kubernetes.io/enforce": "restricted"})
	ctx := context.Background()

	d := deployWorkload(t, client, ns, "good", 1, restrictedCompliantPodSpec(), nil)
	watchAndExpectSuccess(t, client, ns, d, 90*time.Second)

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.PodSecurityAdmission{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a restricted-compliant template must produce no finding, got %+v", res)
	}
}

// TestCheckE2E_PodSecurityAdmission_BaselineEnforce_OnlyRestrictedViolation_NoFinding
// is the anti-false-positive test: a template that runs as root with no
// seccomp profile violates *restricted* but not *baseline*. Under
// enforce: baseline it rolls out fine and must produce no finding.
func TestCheckE2E_PodSecurityAdmission_BaselineEnforce_OnlyRestrictedViolation_NoFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespaceLabeled(t, client, map[string]string{"pod-security.kubernetes.io/enforce": "baseline"})
	ctx := context.Background()

	d := deployWorkload(t, client, ns, "rootish", 1, plainRootPodSpec(), nil)
	watchAndExpectSuccess(t, client, ns, d, 90*time.Second)

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.PodSecurityAdmission{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a template violating only restricted, under enforce: baseline, must produce no finding, got %+v", res)
	}
}

// TestCheckE2E_PodSecurityAdmission_WarnOnly_NoFinding proves warn/audit
// never gate a finding: the same violating template under warn: restricted
// (no enforce label) rolls out normally and the check stays silent.
func TestCheckE2E_PodSecurityAdmission_WarnOnly_NoFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespaceLabeled(t, client, map[string]string{
		"pod-security.kubernetes.io/warn":  "restricted",
		"pod-security.kubernetes.io/audit": "restricted",
	})
	ctx := context.Background()

	d := deployWorkload(t, client, ns, "warned", 1, plainRootPodSpec(), nil)
	watchAndExpectSuccess(t, client, ns, d, 90*time.Second)

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.PodSecurityAdmission{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("warn/audit labels must never produce a finding, got %+v", res)
	}
}

// TestCheckE2E_PodSecurityAdmission_StatefulSetVolumeClaimTemplate_NoFinding
// is a permanent regression guard for the template-as-written
// approximation: the StatefulSet controller injects a persistentVolumeClaim
// volume into the real pod that is not in spec.template.spec.volumes, and
// restricted's volume allowlist permits it. A compliant StatefulSet with a
// volumeClaimTemplate must still produce no finding.
func TestCheckE2E_PodSecurityAdmission_StatefulSetVolumeClaimTemplate_NoFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespaceLabeled(t, client, map[string]string{"pod-security.kubernetes.io/enforce": "restricted"})
	ctx := context.Background()

	podSpec := restrictedCompliantPodSpec()
	podSpec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}
	sts := deployStatefulSet(t, client, ns, "stateful-good", 1, podSpec, func(s *appsv1.StatefulSet) {
		s.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("64Mi")},
				},
			},
		}}
	})

	live, err := client.AppsV1().StatefulSets(ns).Get(ctx, sts.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading StatefulSet %s/%s: %v", ns, sts.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromStatefulSet(live), Client: client}
	res, err := check.PodSecurityAdmission{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a compliant StatefulSet with a volumeClaimTemplate must produce no finding, got %+v", res)
	}
}
