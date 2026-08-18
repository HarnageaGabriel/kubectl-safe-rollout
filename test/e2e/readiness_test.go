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

// TestWatchE2E_ReadinessProbeFailing verifies readiness-probe-failing:
// the container remains running while its readiness probe always fails.
// Classification waits for the Deployment progress deadline.
func TestWatchE2E_ReadinessProbeFailing(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "sleep 3600"},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "exit 1"}},
				},
				PeriodSeconds:    1,
				FailureThreshold: 1,
			},
		}},
	}
	d := deployWorkload(t, client, ns, "readiness-failing", 1, podSpec, func(d *appsv1.Deployment) {
		seconds := int32(60)
		d.Spec.ProgressDeadlineSeconds = &seconds
	})

	watchAndExpectCause(t, client, ns, d, diagnose.CauseReadinessProbeFailing, 3*time.Minute)
}

// TestWatchE2E_SlowReadinessCompletesWithoutFinding verifies that a
// slow-starting application is not classified from transient pod state.
// Its readiness probe fails for about 25 seconds, then the rollout succeeds.
func TestWatchE2E_SlowReadinessCompletesWithoutFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "sleep 25; touch /tmp/ready; sleep 3600"},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "test -f /tmp/ready"}},
				},
				PeriodSeconds:    1,
				FailureThreshold: 1,
			},
		}},
	}
	d := deployWorkload(t, client, ns, "readiness-slow-success", 1, podSpec, func(d *appsv1.Deployment) {
		seconds := int32(120)
		d.Spec.ProgressDeadlineSeconds = &seconds
	})

	watchAndExpectSuccess(t, client, ns, d, 2*time.Minute)
}
