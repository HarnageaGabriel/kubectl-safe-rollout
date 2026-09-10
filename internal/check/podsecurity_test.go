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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

const (
	psaEnforceLabel = "pod-security.kubernetes.io/enforce"
	psaWarnLabel    = "pod-security.kubernetes.io/warn"
	psaAuditLabel   = "pod-security.kubernetes.io/audit"
)

func namespaceWithLabels(labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace, Labels: labels}}
}

// restrictedCompliantSecurityContext returns a container SecurityContext that
// satisfies the "restricted" Pod Security Standard on its own (pod-level
// fields are not needed when every container sets these).
func restrictedCompliantSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		RunAsNonRoot:             boolPtr(true),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func runPodSecurityCheck(t *testing.T, w workload.Workload, nsLabels map[string]string, objs ...runtime.Object) check.Result {
	t.Helper()
	all := append([]runtime.Object{namespaceWithLabels(nsLabels)}, objs...)
	client := fake.NewSimpleClientset(all...)
	res, err := check.PodSecurityAdmission{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  w,
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

// TestPodSecurityAdmission_RestrictedEnforce_PrivilegedContainer_High is the
// core positive case: the namespace enforces "restricted", the pod template
// runs a privileged container, admission would reject every pod.
func TestPodSecurityAdmission_RestrictedEnforce_PrivilegedContainer_High(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:            "app",
		SecurityContext: &corev1.SecurityContext{Privileged: boolPtr(true)},
	})

	res := runPodSecurityCheck(t, workload.FromDeployment(d), map[string]string{psaEnforceLabel: "restricted"}, d)

	if res.Skipped {
		t.Fatalf("must not be skipped, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != check.PodSecurityAdmissionCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "restricted") {
		t.Errorf("cause must name the enforced level: %q", f.Cause)
	}
	if !containsEvidence(f.Evidence, "enforceLevel=restricted") {
		t.Errorf("evidence must record the enforce level, got %v", f.Evidence)
	}
	if !hasEvidencePrefix(f.Evidence, "evaluatedPolicyVersion=restricted:") {
		t.Errorf("evidence must record the evaluated policy version, got %v", f.Evidence)
	}
	if len(f.Evidence) < 3 {
		t.Errorf("evidence must include at least one verbatim forbidden reason, got %v", f.Evidence)
	}
	if !f.Remediation.ContextDependent || len(f.Remediation.Commands) != 1 || !strings.Contains(f.Remediation.Commands[0], "kubectl get namespace") {
		t.Errorf("remediation must be context-dependent with a single read-only namespace command, got %+v", f.Remediation)
	}
	if f.Resource.Kind != "Deployment" || f.Resource.Name != "checkout" || f.Resource.Namespace != testNamespace {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

// TestPodSecurityAdmission_BaselineEnforce_HostNetwork_High proves the check
// evaluates against the level the label actually names: hostNetwork violates
// "baseline" (and "restricted"), and the finding must fire under a "baseline"
// label, not only "restricted".
func TestPodSecurityAdmission_BaselineEnforce_HostNetwork_High(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})
	d.Spec.Template.Spec.HostNetwork = true

	res := runPodSecurityCheck(t, workload.FromDeployment(d), map[string]string{psaEnforceLabel: "baseline"}, d)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if !strings.Contains(f.Cause, "baseline") {
		t.Errorf("cause must name the baseline level actually enforced, not a hardcoded one: %q", f.Cause)
	}
	if !containsEvidence(f.Evidence, "enforceLevel=baseline") {
		t.Errorf("evidence must record enforceLevel=baseline, got %v", f.Evidence)
	}
}

// TestPodSecurityAdmission_RestrictedEnforce_CompliantTemplate_NoFinding
// covers the "no problem" path: a fully restricted-compliant template under a
// "restricted" label must produce no finding and must not be skipped.
func TestPodSecurityAdmission_RestrictedEnforce_CompliantTemplate_NoFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:            "app",
		SecurityContext: restrictedCompliantSecurityContext(),
	})

	res := runPodSecurityCheck(t, workload.FromDeployment(d), map[string]string{psaEnforceLabel: "restricted"}, d)

	if res.Skipped {
		t.Fatalf("must not be skipped for a readable namespace, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings for a restricted-compliant template, got %+v", res.Findings)
	}
}

// TestPodSecurityAdmission_BaselineEnforce_TemplateViolatingOnlyRestricted_NoFinding
// mirrors the live P0-c observation: a template that runs as root with no
// seccomp profile violates "restricted" but is admitted under "baseline", so
// under a "baseline" label the check must stay silent.
func TestPodSecurityAdmission_BaselineEnforce_TemplateViolatingOnlyRestricted_NoFinding(t *testing.T) {
	// A bare container: runs as root, no seccomp profile, no
	// allowPrivilegeEscalation=false, no dropped capabilities — every one a
	// "restricted"-only requirement. It sets nothing that "baseline"
	// forbids (not privileged, no host namespaces, no hostPath, no
	// hostPort).
	d := deploymentWithContainers(corev1.Container{Name: "app"})

	res := runPodSecurityCheck(t, workload.FromDeployment(d), map[string]string{psaEnforceLabel: "baseline"}, d)

	if res.Skipped {
		t.Fatalf("must not be skipped, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a restricted-only violation must not fire under a baseline label, got %+v", res.Findings)
	}
}

// TestPodSecurityAdmission_NoEnforceLabel_WarnAndAuditRestricted_NoFinding
// covers the binding decision that only the enforce label gates a finding:
// warn/audit are visible elsewhere and never produce one.
func TestPodSecurityAdmission_NoEnforceLabel_WarnAndAuditRestricted_NoFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:            "app",
		SecurityContext: &corev1.SecurityContext{Privileged: boolPtr(true)},
	})

	res := runPodSecurityCheck(t, workload.FromDeployment(d), map[string]string{
		psaWarnLabel:  "restricted",
		psaAuditLabel: "restricted",
	}, d)

	if res.Skipped {
		t.Fatalf("a namespace with no enforce label must not be skipped, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("warn/audit labels must not produce a finding, got %+v", res.Findings)
	}
}

// TestPodSecurityAdmission_EnforcePrivileged_PrivilegedContainer_NoFinding
// covers the early return for the privileged level, which admits everything.
func TestPodSecurityAdmission_EnforcePrivileged_PrivilegedContainer_NoFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:            "app",
		SecurityContext: &corev1.SecurityContext{Privileged: boolPtr(true)},
	})

	res := runPodSecurityCheck(t, workload.FromDeployment(d), map[string]string{psaEnforceLabel: "privileged"}, d)

	if res.Skipped {
		t.Fatalf("must not be skipped, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("the privileged level admits every pod: want 0 findings, got %+v", res.Findings)
	}
}

// TestPodSecurityAdmission_NamespaceGetForbidden_Skips covers graceful
// degradation: the namespace Get is the check's only live read, and a denied
// read must Skip with a reason, never fail the run.
func TestPodSecurityAdmission_NamespaceGetForbidden_Skips(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})
	client := fake.NewSimpleClientset(d)
	client.PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: cannot get resource \"namespaces\"")
	})

	res, err := check.PodSecurityAdmission{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must degrade to Skipped on a denied namespace read, not return an error: %v", err)
	}
	if !res.Skipped {
		t.Fatal("want Skipped=true when the namespace is not readable")
	}
	if strings.TrimSpace(res.SkipReason) == "" {
		t.Error("SkipReason must not be empty")
	}
}

// TestPodSecurityAdmission_UnparseableEnforceLabel_Skips covers the defensive
// branch for a label value that cannot be parsed. This is unreachable on a
// real cluster (the apiserver rejects such a value at namespace write time),
// so the check treats it as "cannot evaluate" rather than as a violation.
func TestPodSecurityAdmission_UnparseableEnforceLabel_Skips(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})

	res := runPodSecurityCheck(t, workload.FromDeployment(d), map[string]string{psaEnforceLabel: "bogus"}, d)

	if !res.Skipped {
		t.Fatalf("an unparseable enforce label must Skip, got findings %+v", res.Findings)
	}
	if strings.TrimSpace(res.SkipReason) == "" {
		t.Error("SkipReason must not be empty")
	}
}

// TestPodSecurityAdmission_StatefulSetTarget_CompliantTemplate_NoFinding
// documents that there is no workload-kind exclusion: a StatefulSet is
// evaluated the same way.
func TestPodSecurityAdmission_StatefulSetTarget_CompliantTemplate_NoFinding(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", SecurityContext: restrictedCompliantSecurityContext()}},
			}},
		},
	}

	res := runPodSecurityCheck(t, workload.FromStatefulSet(s), map[string]string{psaEnforceLabel: "restricted"}, s)

	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("want a clean, non-skipped result for a compliant StatefulSet, got skipped=%v findings=%+v", res.Skipped, res.Findings)
	}
}

// TestPodSecurityAdmission_DaemonSetTarget_CompliantTemplate_NoFinding is the
// DaemonSet counterpart of the StatefulSet case above.
func TestPodSecurityAdmission_DaemonSetTarget_CompliantTemplate_NoFinding(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: testNamespace},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", SecurityContext: restrictedCompliantSecurityContext()}},
			}},
		},
	}

	res := runPodSecurityCheck(t, workload.FromDaemonSet(ds), map[string]string{psaEnforceLabel: "restricted"}, ds)

	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("want a clean, non-skipped result for a compliant DaemonSet, got skipped=%v findings=%+v", res.Skipped, res.Findings)
	}
}

// TestPodSecurityAdmission_RestrictedEnforce_ViolatingInitContainer_High
// guards against the "regular containers only" class of bug already seen in
// resource-limits: the evaluator receives the whole PodSpec, so an init
// container that violates the policy must still produce a High finding even
// when every regular container is compliant.
func TestPodSecurityAdmission_RestrictedEnforce_ViolatingInitContainer_High(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:            "app",
		SecurityContext: restrictedCompliantSecurityContext(),
	})
	d.Spec.Template.Spec.InitContainers = []corev1.Container{{
		Name:            "setup",
		SecurityContext: &corev1.SecurityContext{Privileged: boolPtr(true)},
	}}

	res := runPodSecurityCheck(t, workload.FromDeployment(d), map[string]string{psaEnforceLabel: "restricted"}, d)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for a violating init container, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

func containsEvidence(evidence []string, want string) bool {
	for _, e := range evidence {
		if e == want {
			return true
		}
	}
	return false
}

func hasEvidencePrefix(evidence []string, prefix string) bool {
	for _, e := range evidence {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
