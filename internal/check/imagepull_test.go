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

func runImagePullSecretsCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.ImagePullSecrets{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestLooksLikePrivateRegistry_ClassifiesImageReferences(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		private bool
	}{
		{name: "Docker Hub image without slash", image: "nginx:1.25", private: false},
		{name: "implicit Docker Hub organization", image: "myorg/app:v1", private: false},
		{name: "explicit Docker Hub", image: "docker.io/myorg/app:v1", private: false},
		{name: "registry with port", image: "registry.internal:5000/app:v1", private: true},
		{name: "registry with dot", image: "gcr.io/progetto/app:v1", private: true},
		{name: "localhost with port", image: "localhost:5000/app:v1", private: true},
		{name: "localhost without port", image: "localhost/app:v1", private: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := check.LooksLikePrivateRegistry(test.image); got != test.private {
				t.Errorf("LooksLikePrivateRegistry(%q) = %t, want %t", test.image, got, test.private)
			}
		})
	}
}

func TestImagePullSecrets_DockerHubImages_NoFindings(t *testing.T) {
	for _, image := range []string{"nginx:1.25", "docker.io/myorg/app:v1"} {
		t.Run(image, func(t *testing.T) {
			result := runImagePullSecretsCheck(t, deploymentWithContainers(corev1.Container{Name: "app", Image: image}))
			if result.Skipped || len(result.Findings) != 0 {
				t.Fatalf("Docker Hub image %q: want empty result, got %+v", image, result)
			}
		})
	}
}

func TestImagePullSecrets_SecretOnPod_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "registry.internal:5000/app:v1"})
	d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry-creds"}}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry-creds", Namespace: testNamespace}}

	result := runImagePullSecretsCheck(t, d, serviceAccount, secret)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("imagePullSecret on Pod, Secret exists: want empty result, got %+v", result)
	}
}

func TestImagePullSecrets_SecretOnServiceAccount_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "gcr.io/progetto/app:v1"})
	d.Spec.Template.Spec.ServiceAccountName = "deployer"
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta:       metav1.ObjectMeta{Name: "deployer", Namespace: testNamespace},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-creds"}},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry-creds", Namespace: testNamespace}}

	result := runImagePullSecretsCheck(t, d, serviceAccount, secret)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("imagePullSecret on ServiceAccount, Secret exists: want empty result, got %+v", result)
	}
}

// Discovered on kind: a Deployment can declare an imagePullSecrets entry
// that does not resolve to any Secret object. The pod ends up in
// ImagePullBackOff while a check that only verifies the name is present
// reports no problem at all.
func TestImagePullSecrets_DeclaredSecretOnPodDoesNotExist_MediumFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "registry.internal:5000/app:v1"})
	d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "totally-does-not-exist"}}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}

	result := runImagePullSecretsCheck(t, d, serviceAccount)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	finding := result.Findings[0]
	if finding.CheckID != check.ImagePullSecretsCheckID || finding.Severity != model.SeverityMedium {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want Medium (a missing declared Secret is a verified fact, not a hostname guess)", finding.CheckID, finding.Severity)
	}
	if !strings.Contains(finding.Cause, "totally-does-not-exist") {
		t.Errorf("cause must name the missing Secret, got %q", finding.Cause)
	}
	if !finding.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent: creating the Secret vs. fixing a typo are different actions this tool cannot choose between")
	}
	if len(finding.Remediation.Commands) == 0 || !strings.Contains(finding.Remediation.Commands[0], "totally-does-not-exist") {
		t.Errorf("remediation command must reference the missing Secret, got %+v", finding.Remediation.Commands)
	}
}

func TestImagePullSecrets_DeclaredSecretOnServiceAccountDoesNotExist_MediumFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "gcr.io/progetto/app:v1"})
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta:       metav1.ObjectMeta{Name: "default", Namespace: testNamespace},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "deleted-secret"}},
	}

	result := runImagePullSecretsCheck(t, d, serviceAccount)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	if result.Findings[0].Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium", result.Findings[0].Severity)
	}
	if !strings.Contains(result.Findings[0].Cause, "deleted-secret") {
		t.Errorf("cause must name the missing Secret, got %q", result.Findings[0].Cause)
	}
}

// One working Secret among several declared names is enough coverage: the
// others may be unused leftovers, not evidence of a problem.
func TestImagePullSecrets_OneOfSeveralDeclaredSecretsExists_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "registry.internal:5000/app:v1"})
	d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
		{Name: "stale-leftover"},
		{Name: "registry-creds"},
	}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry-creds", Namespace: testNamespace}}

	result := runImagePullSecretsCheck(t, d, serviceAccount, secret)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("one of several declared Secrets exists: want empty result, got %+v", result)
	}
}

func TestImagePullSecrets_SecretReadFailed_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "registry.internal:5000/app:v1"})
	d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry-creds"}}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}
	client := fake.NewSimpleClientset(serviceAccount)
	client.PrependReactor("get", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading the Secret")
	})

	result, err := check.ImagePullSecrets{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the Secret is not accessible: an RBAC gap must not be reported as a missing Secret")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

func TestImagePullSecrets_SecretMissing_LowContextDependent(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "gcr.io/progetto/app:v1"})
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}

	result := runImagePullSecretsCheck(t, d, serviceAccount)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	finding := result.Findings[0]
	if finding.CheckID != check.ImagePullSecretsCheckID || finding.Severity != model.SeverityLow {
		t.Errorf("unexpected finding: checkID=%q severity=%v", finding.CheckID, finding.Severity)
	}
	if !finding.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
	if !strings.Contains(finding.Cause, "app") || !strings.Contains(finding.Cause, "gcr.io/progetto/app:v1") || !strings.Contains(finding.Cause, "hostname") {
		t.Errorf("cause does not state container, image, and heuristic: %q", finding.Cause)
	}
	if !strings.Contains(finding.Remediation.Summary, "false positive") || !strings.Contains(finding.Remediation.Summary, "hostname") {
		t.Errorf("remediation does not state the possible false positive: %q", finding.Remediation.Summary)
	}
	wantEvidence := []string{"container=app", "image=gcr.io/progetto/app:v1", "serviceAccount=default"}
	if len(finding.Evidence) != len(wantEvidence) {
		t.Fatalf("evidence = %+v, want %d entries", finding.Evidence, len(wantEvidence))
	}
	for i, want := range wantEvidence {
		if finding.Evidence[i] != want {
			t.Errorf("evidence[%d] = %q, want %q", i, finding.Evidence[i], want)
		}
	}
	if finding.Resource.Kind != "Pod" || finding.Resource.Namespace != testNamespace || finding.Resource.Name != "checkout/app" {
		t.Errorf("unexpected resource ref: %+v", finding.Resource)
	}
}

func TestImagePullSecrets_ServiceAccountReadFailed_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "registry.internal:5000/app:v1"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading the ServiceAccount")
	})

	result, err := check.ImagePullSecrets{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the ServiceAccount is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
