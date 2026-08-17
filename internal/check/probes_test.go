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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func deploymentWithContainers(containers ...corev1.Container) *appsv1.Deployment {
	d := deployment(1, rollingUpdateStrategy(nil))
	d.Spec.Template.Spec.Containers = containers
	return d
}

func runProbeCheck(t *testing.T, d *appsv1.Deployment) check.Result {
	t.Helper()
	res, err := check.ProbeSanity{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
	})
	if err != nil {
		t.Fatalf("Run() ha restituito un errore inatteso: %v", err)
	}
	return res
}

func TestProbeSanity_ReadinessAssente_Low(t *testing.T) {
	res := runProbeCheck(t, deploymentWithContainers(corev1.Container{
		Name:          "app",
		LivenessProbe: &corev1.Probe{},
	}))

	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding per readinessProbe assente, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != check.ProbeSanityCheckID || f.Severity != model.SeverityLow {
		t.Errorf("finding inatteso: checkID=%q severity=%v", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "app") || !strings.Contains(f.Cause, "readinessProbe") {
		t.Errorf("cause non identifica container e probe mancanti: %q", f.Cause)
	}
	if len(f.Evidence) == 0 || f.Evidence[0] != "container=app" {
		t.Errorf("evidence = %+v, atteso container=app", f.Evidence)
	}
	if !f.Remediation.ContextDependent {
		t.Error("la remediation della probe deve dichiararsi context-dependent")
	}
	if f.Resource.Kind != "Pod" || f.Resource.Namespace != testNamespace || f.Resource.Name != "checkout/app" {
		t.Errorf("resource ref inatteso: %+v", f.Resource)
	}
}

func TestProbeSanity_LivenessAssente_Low(t *testing.T) {
	res := runProbeCheck(t, deploymentWithContainers(corev1.Container{
		Name:           "app",
		ReadinessProbe: &corev1.Probe{},
	}))

	if len(res.Findings) != 1 {
		t.Fatalf("atteso 1 finding per livenessProbe assente, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityLow || !strings.Contains(f.Cause, "livenessProbe") {
		t.Errorf("finding inatteso: %+v", f)
	}
}

func TestProbeSanity_EntrambeLeProbeAssenti_DueFinding(t *testing.T) {
	res := runProbeCheck(t, deploymentWithContainers(corev1.Container{Name: "app"}))

	if len(res.Findings) != 2 {
		t.Fatalf("attesi 2 finding con entrambe le probe assenti, got %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity != model.SeverityLow {
			t.Errorf("severity = %v, atteso Low", f.Severity)
		}
	}
}

func TestProbeSanity_EntrambeLeProbePresenti_NessunFinding(t *testing.T) {
	res := runProbeCheck(t, deploymentWithContainers(corev1.Container{
		Name:           "app",
		ReadinessProbe: &corev1.Probe{},
		LivenessProbe:  &corev1.Probe{},
	}))

	if len(res.Findings) != 0 {
		t.Fatalf("attesi 0 finding con entrambe le probe presenti, got %+v", res.Findings)
	}
}
