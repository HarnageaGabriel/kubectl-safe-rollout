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

// TestWatchE2E_ProgressDeadlineExceeded verifies progress-deadline-
// exceeded in isolation from the other causes: the container starts and
// keeps running (no CrashLoopBackOff, no pull problem, no blocked
// scheduling), but the readinessProbe always fails, so the pod never
// becomes Ready/Available. With a short progressDeadlineSeconds, the
// Deployment controller marks the Progressing condition as
// ProgressDeadlineExceeded before any other Diagnoser has anything to
// report.
func TestWatchE2E_ProgressDeadlineExceeded(t *testing.T) {
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
				InitialDelaySeconds: 0,
				PeriodSeconds:       1,
				FailureThreshold:    1,
			},
		}},
	}
	d := deployWorkload(t, client, ns, "progress-deadline", 1, podSpec, func(d *appsv1.Deployment) {
		seconds := int32(10)
		d.Spec.ProgressDeadlineSeconds = &seconds
	})

	watchAndExpectCause(t, client, ns, d, diagnose.CauseProgressDeadlineExceeded, 2*time.Minute)
}
