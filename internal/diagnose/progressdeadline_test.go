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

package diagnose_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func deploymentWithConditions(conditions ...appsv1.DeploymentCondition) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(3)},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Conditions:         conditions,
		},
	}
}

func TestProgressDeadline_Exceeded(t *testing.T) {
	d := deploymentWithConditions(appsv1.DeploymentCondition{
		Type:    appsv1.DeploymentProgressing,
		Reason:  "ProgressDeadlineExceeded",
		Message: "ReplicaSet \"app-abc123\" has timed out progressing.",
	})
	target := newTarget(t, nil, nil, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ProgressDeadline{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseProgressDeadlineExceeded) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseProgressDeadlineExceeded, res.Findings)
	}
	if res.Findings[0].Undetermined {
		t.Error("the ProgressDeadlineExceeded condition is already the cause: it must not be Undetermined")
	}
}

func TestProgressDeadline_NotExceeded(t *testing.T) {
	d := deploymentWithConditions(appsv1.DeploymentCondition{
		Type:   appsv1.DeploymentProgressing,
		Reason: "ReplicaSetUpdated",
	})
	target := newTarget(t, nil, nil, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ProgressDeadline{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a rollout still in progress must not produce findings, got %+v", res.Findings)
	}
}
