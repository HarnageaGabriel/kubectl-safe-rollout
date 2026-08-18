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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func runResourceLimitsCheck(t *testing.T, d *appsv1.Deployment) check.Result {
	t.Helper()
	res, err := check.ResourceLimits{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

func TestResourceLimits_CPULimitMissing_Low(t *testing.T) {
	res := runResourceLimitsCheck(t, deploymentWithContainers(corev1.Container{
		Name: "app",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		}},
	}))

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for missing CPU limit, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != check.ResourceLimitsCheckID || f.Severity != model.SeverityLow {
		t.Errorf("unexpected finding: checkID=%q severity=%v", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "app") || !strings.Contains(f.Cause, "CPU") {
		t.Errorf("cause does not identify the container and missing limit: %q", f.Cause)
	}
	if len(f.Evidence) == 0 || f.Evidence[0] != "container=app" {
		t.Errorf("evidence = %+v, want container=app", f.Evidence)
	}
	if !f.Remediation.ContextDependent {
		t.Error("limit remediation must declare itself context-dependent")
	}
	if f.Resource.Kind != "Pod" || f.Resource.Namespace != testNamespace || f.Resource.Name != "checkout/app" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

func TestResourceLimits_MemoryLimitMissing_Low(t *testing.T) {
	res := runResourceLimitsCheck(t, deploymentWithContainers(corev1.Container{
		Name: "app",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}))

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for missing memory limit, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityLow || !strings.Contains(f.Cause, "memory") {
		t.Errorf("unexpected finding: %+v", f)
	}
	if !strings.Contains(f.Remediation.Summary, "OOMKill") {
		t.Errorf("memory remediation does not mention OOMKill risk: %q", f.Remediation.Summary)
	}
}

func TestResourceLimits_BothLimitsMissing_TwoFindings(t *testing.T) {
	res := runResourceLimitsCheck(t, deploymentWithContainers(corev1.Container{Name: "app"}))

	if len(res.Findings) != 2 {
		t.Fatalf("want 2 findings when both limits are missing, got %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity != model.SeverityLow {
			t.Errorf("severity = %v, want Low", f.Severity)
		}
	}
}

func TestResourceLimits_BothLimitsPresent_NoFindings(t *testing.T) {
	res := runResourceLimitsCheck(t, deploymentWithContainers(corev1.Container{
		Name: "app",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		}},
	}))

	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings when both limits are present, got %+v", res.Findings)
	}
}
