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
		t.Fatalf("Run() ha restituito un errore inatteso: %v", err)
	}
	return result
}

func TestLooksLikePrivateRegistry_ClassificaRiferimentiImmagine(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		private bool
	}{
		{name: "immagine Docker Hub senza slash", image: "nginx:1.25", private: false},
		{name: "organizzazione Docker Hub implicita", image: "myorg/app:v1", private: false},
		{name: "Docker Hub esplicito", image: "docker.io/myorg/app:v1", private: false},
		{name: "registry con porta", image: "registry.internal:5000/app:v1", private: true},
		{name: "registry con punto", image: "gcr.io/progetto/app:v1", private: true},
		{name: "localhost con porta", image: "localhost:5000/app:v1", private: true},
		{name: "localhost senza porta", image: "localhost/app:v1", private: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := check.LooksLikePrivateRegistry(test.image); got != test.private {
				t.Errorf("LooksLikePrivateRegistry(%q) = %t, atteso %t", test.image, got, test.private)
			}
		})
	}
}

func TestImagePullSecrets_ImmaginiDockerHub_NessunFinding(t *testing.T) {
	for _, image := range []string{"nginx:1.25", "docker.io/myorg/app:v1"} {
		t.Run(image, func(t *testing.T) {
			result := runImagePullSecretsCheck(t, deploymentWithContainers(corev1.Container{Name: "app", Image: image}))
			if result.Skipped || len(result.Findings) != 0 {
				t.Fatalf("immagine Docker Hub %q: atteso risultato vuoto, got %+v", image, result)
			}
		})
	}
}

func TestImagePullSecrets_SecretSulPod_NessunFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "registry.internal:5000/app:v1"})
	d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry-creds"}}

	result := runImagePullSecretsCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("imagePullSecret sul Pod: atteso risultato vuoto, got %+v", result)
	}
}

func TestImagePullSecrets_SecretSulServiceAccount_NessunFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "gcr.io/progetto/app:v1"})
	d.Spec.Template.Spec.ServiceAccountName = "deployer"
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta:       metav1.ObjectMeta{Name: "deployer", Namespace: testNamespace},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-creds"}},
	}

	result := runImagePullSecretsCheck(t, d, serviceAccount)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("imagePullSecret sul ServiceAccount: atteso risultato vuoto, got %+v", result)
	}
}

func TestImagePullSecrets_SecretAssente_LowContextDependent(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "gcr.io/progetto/app:v1"})
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}

	result := runImagePullSecretsCheck(t, d, serviceAccount)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("atteso un finding non skipped, got %+v", result)
	}
	finding := result.Findings[0]
	if finding.CheckID != check.ImagePullSecretsCheckID || finding.Severity != model.SeverityLow {
		t.Errorf("finding inatteso: checkID=%q severity=%v", finding.CheckID, finding.Severity)
	}
	if !finding.Remediation.ContextDependent {
		t.Error("la remediation deve dichiararsi context-dependent")
	}
	if !strings.Contains(finding.Cause, "app") || !strings.Contains(finding.Cause, "gcr.io/progetto/app:v1") || !strings.Contains(finding.Cause, "hostname") {
		t.Errorf("cause non esplicita container, immagine ed euristica: %q", finding.Cause)
	}
	if !strings.Contains(finding.Remediation.Summary, "falso positivo") || !strings.Contains(finding.Remediation.Summary, "hostname") {
		t.Errorf("remediation non esplicita il possibile falso positivo: %q", finding.Remediation.Summary)
	}
	wantEvidence := []string{"container=app", "image=gcr.io/progetto/app:v1", "serviceAccount=default"}
	if len(finding.Evidence) != len(wantEvidence) {
		t.Fatalf("evidence = %+v, attese %d voci", finding.Evidence, len(wantEvidence))
	}
	for i, want := range wantEvidence {
		if finding.Evidence[i] != want {
			t.Errorf("evidence[%d] = %q, atteso %q", i, finding.Evidence[i], want)
		}
	}
	if finding.Resource.Kind != "Pod" || finding.Resource.Namespace != testNamespace || finding.Resource.Name != "checkout/app" {
		t.Errorf("resource ref inatteso: %+v", finding.Resource)
	}
}

func TestImagePullSecrets_LetturaServiceAccountFallita_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "registry.internal:5000/app:v1"})
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC nega la lettura del ServiceAccount")
	})

	result, err := check.ImagePullSecrets{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() non deve restituire errore su lettura fallita, deve degradare a Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("atteso Skipped=true quando il ServiceAccount non e' accessibile")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason non deve essere vuoto")
	}
}
