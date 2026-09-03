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
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func runPriorityClassExistsCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.PriorityClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestPriorityClassExists_Unset_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})

	result := runPriorityClassExistsCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("unset priorityClassName: want empty result, got %+v", result)
	}
}

func TestPriorityClassExists_ExplicitAndExisting_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.PriorityClassName = "high-priority"
	pc := &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: "high-priority"}, Value: 1000000}

	result := runPriorityClassExistsCheck(t, d, pc)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("explicit, existing PriorityClass: want empty result, got %+v", result)
	}
}

// The two system special keywords are guaranteed valid by the PodSpec API
// contract itself (see the doc comment on PriorityClassExists), independent
// of whether a PriorityClass object with that literal name exists: they
// must never be Get'd, let alone flagged as missing.
func TestPriorityClassExists_SystemCriticalKeywords_NotFlagged(t *testing.T) {
	for _, name := range []string{"system-node-critical", "system-cluster-critical"} {
		d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
		d.Spec.Template.Spec.PriorityClassName = name

		result := runPriorityClassExistsCheck(t, d)
		if result.Skipped || len(result.Findings) != 0 {
			t.Fatalf("system special keyword %q: want empty result without ever calling the API, got %+v", name, result)
		}
	}
}

func TestPriorityClassExists_Missing_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.PriorityClassName = "this-priorityclass-does-not-exist"

	result := runPriorityClassExistsCheck(t, d)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.PriorityClassExistsCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "this-priorityclass-does-not-exist") {
		t.Errorf("cause must name the missing PriorityClass, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent: creating the PriorityClass vs. fixing a typo are different actions this tool cannot choose between")
	}
	if len(f.Remediation.Commands) != 1 || !strings.Contains(f.Remediation.Commands[0], "this-priorityclass-does-not-exist") {
		t.Errorf("remediation command must be a read-only inspection of the named PriorityClass, got %+v", f.Remediation.Commands)
	}
	if f.Resource.Kind != "PriorityClass" || f.Resource.Name != "this-priorityclass-does-not-exist" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
	if f.Resource.Namespace != "" {
		t.Errorf("PriorityClass is cluster-scoped: resource ref must not carry a namespace, got %+v", f.Resource)
	}
}

func TestPriorityClassExists_StatefulSet_Missing_HighFinding(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers:        []corev1.Container{{Name: "app", Image: "example.com/db:v1"}},
					PriorityClassName: "this-priorityclass-does-not-exist",
				},
			},
		},
	}
	client := fake.NewSimpleClientset()
	result, err := check.PriorityClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromStatefulSet(s),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the StatefulSet-backed target, got %+v", result)
	}
	if !strings.Contains(result.Findings[0].Cause, "StatefulSet/db") {
		t.Errorf("cause must name the StatefulSet workload, got %q", result.Findings[0].Cause)
	}
}

func TestPriorityClassExists_StatefulSet_Existing_NoFindings(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers:        []corev1.Container{{Name: "app", Image: "example.com/db:v1"}},
					PriorityClassName: "high-priority",
				},
			},
		},
	}
	pc := &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: "high-priority"}, Value: 1000000}
	client := fake.NewSimpleClientset(pc)
	result, err := check.PriorityClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromStatefulSet(s),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("existing PriorityClass on a StatefulSet-backed target: want empty result, got %+v", result)
	}
}

func TestPriorityClassExists_ReadFailed_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.PriorityClassName = "high-priority"
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "priorityclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading PriorityClasses")
	})

	result, err := check.PriorityClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the PriorityClass is not accessible: an RBAC gap must not be reported as a missing PriorityClass")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
