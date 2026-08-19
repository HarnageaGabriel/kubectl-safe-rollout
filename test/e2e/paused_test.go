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
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
)

// Discovered running `watch` by hand against a real paused Deployment on
// kind, before the Paused Diagnoser existed: with no symptom for any other
// Diagnoser to see and progressDeadlineSeconds itself frozen by Kubernetes
// while paused, watch hung indefinitely with no output at all. Verified with
// `timeout 40s watch ...`: process killed after the full 40s, zero lines
// printed. This scenario is the regression guard for that.
func TestWatchE2E_RolloutPaused(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "nginx:1.27",
		}},
	}
	d := deployWorkload(t, client, ns, "rollout-paused", 2, podSpec, func(d *appsv1.Deployment) {
		d.Spec.Paused = true
	})

	watchAndExpectCause(t, client, ns, d, diagnose.CauseRolloutPaused, 2*time.Minute)
}
