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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func boolPtrCfg(v bool) *bool { return &v }

func runConfigRefsCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.ConfigReferencesExist{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestConfigReferencesExist_EnvFromConfigMapMissing_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:    "app",
		Image:   "nginx:1.27",
		EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}},
	})

	result := runConfigRefsCheck(t, d)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the missing ConfigMap, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.ConfigReferencesExistCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "app-config") {
		t.Errorf("cause must name the missing ConfigMap, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
	if f.Resource.Kind != "ConfigMap" || f.Resource.Name != "app-config" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

func TestConfigReferencesExist_EnvFromConfigMapExists_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:    "app",
		Image:   "nginx:1.27",
		EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}},
	})
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: testNamespace}}

	result := runConfigRefsCheck(t, d, cm)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("ConfigMap exists: want empty result, got %+v", result)
	}
}

func TestConfigReferencesExist_EnvFromSecretMissing_Optional_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:    "app",
		Image:   "nginx:1.27",
		EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"}, Optional: boolPtrCfg(true)}}},
	})

	result := runConfigRefsCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("a reference marked Optional must not be reported when missing: Kubernetes itself treats it as non-fatal, got %+v", result)
	}
}

func TestConfigReferencesExist_EnvValueFromConfigMapKeyMissing_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:  "app",
		Image: "nginx:1.27",
		Env: []corev1.EnvVar{{
			Name: "DB_HOST",
			ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}, Key: "db.host"},
			},
		}},
	})
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: testNamespace},
		Data:       map[string]string{"other.key": "value"},
	}

	result := runConfigRefsCheck(t, d, cm)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the missing key, got %+v", result)
	}
	f := result.Findings[0]
	if !strings.Contains(f.Cause, "db.host") || !strings.Contains(f.Cause, "app-config") {
		t.Errorf("cause must name the missing key and the ConfigMap, got %q", f.Cause)
	}
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: a missing referenced key deterministically blocks container start", f.Severity)
	}
}

func TestConfigReferencesExist_EnvValueFromConfigMapKeyExists_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:  "app",
		Image: "nginx:1.27",
		Env: []corev1.EnvVar{{
			Name: "DB_HOST",
			ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}, Key: "db.host"},
			},
		}},
	})
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: testNamespace},
		Data:       map[string]string{"db.host": "postgres"},
	}

	result := runConfigRefsCheck(t, d, cm)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("referenced key exists: want empty result, got %+v", result)
	}
}

func TestConfigReferencesExist_SecretKeyRefMissingSecret_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:  "app",
		Image: "nginx:1.27",
		Env: []corev1.EnvVar{{
			Name: "DB_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"}, Key: "password"},
			},
		}},
	})

	result := runConfigRefsCheck(t, d)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the missing Secret, got %+v", result)
	}
	if result.Findings[0].Resource.Kind != "Secret" {
		t.Errorf("unexpected resource ref: %+v", result.Findings[0].Resource)
	}
}

func TestConfigReferencesExist_VolumeConfigMapMissing_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name:         "config",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}},
	}}

	result := runConfigRefsCheck(t, d)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the missing volume ConfigMap, got %+v", result)
	}
	if !strings.Contains(result.Findings[0].Cause, "volume") {
		t.Errorf("cause must name the volume as the source, got %q", result.Findings[0].Cause)
	}
}

func TestConfigReferencesExist_VolumeSecretItemKeyMissing_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name: "tls",
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: "app-tls",
			Items:      []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}},
		}},
	}}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-tls", Namespace: testNamespace},
		Data:       map[string][]byte{"tls.key": []byte("x")},
	}

	result := runConfigRefsCheck(t, d, secret)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the missing item key, got %+v", result)
	}
	if !strings.Contains(result.Findings[0].Cause, "tls.crt") {
		t.Errorf("cause must name the missing key, got %q", result.Findings[0].Cause)
	}
}

func TestConfigReferencesExist_VolumeConfigMapOptionalMissing_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name: "config",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
			Optional:             boolPtrCfg(true),
		}},
	}}

	result := runConfigRefsCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("Optional volume source missing: want empty result, got %+v", result)
	}
}

func TestConfigReferencesExist_NoReferences_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})

	result := runConfigRefsCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no ConfigMap/Secret references at all: want empty result, got %+v", result)
	}
}

func TestConfigReferencesExist_SameConfigMapReferencedTwice_OneGetOnly(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:  "app",
		Image: "nginx:1.27",
		Env: []corev1.EnvVar{
			{Name: "A", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}, Key: "a"}}},
			{Name: "B", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}, Key: "b"}}},
		},
	})
	client := fake.NewSimpleClientset()
	gets := 0
	client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "app-config")
	})

	result, err := check.ConfigReferencesExist{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("two keys on the same missing ConfigMap must produce one finding (object-level), not one per key, got %+v", result.Findings)
	}
	if gets != 1 {
		t.Errorf("the same ConfigMap name must be resolved once and cached, got %d Get calls", gets)
	}
}

func TestConfigReferencesExist_ReadFailed_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:    "app",
		Image:   "nginx:1.27",
		EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}},
	})
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading ConfigMaps")
	})

	result, err := check.ConfigReferencesExist{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the ConfigMap is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
