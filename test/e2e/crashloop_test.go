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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
)

// TestWatchE2E_CrashLoopApplicationExit verifies crashloop-app-error:
// a container that exits immediately with an application exit code,
// neither OOMKilled nor killed by the liveness probe.
func TestWatchE2E_CrashLoopApplicationExit(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "exit 7"},
		}},
	}
	d := deployWorkload(t, client, ns, "crashy-app-error", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseCrashLoopAppError, 2*time.Minute)
}

// TestWatchE2E_CrashLoopOOMKilled verifies crashloop-oomkilled: a
// container with a low memory limit and a command that allocates more
// and more memory until the kernel terminates it.
func TestWatchE2E_CrashLoopOOMKilled(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "x=A; while true; do x=$x$x$x$x; done"},
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("16Mi"),
				},
			},
		}},
	}
	d := deployWorkload(t, client, ns, "crashy-oom", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseCrashLoopOOMKilled, 2*time.Minute)
}

// TestWatchE2E_CrashLoopLivenessProbe verifies
// crashloop-liveness-probe: a container that starts and keeps running,
// but whose liveness probe points to a path that always returns 404 with
// a low failure threshold, so the kubelet repeatedly terminates it
// before it can crash on its own.
func TestWatchE2E_CrashLoopLivenessProbe(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "nginx:1.25",
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{Path: "/questo-path-non-esiste", Port: intstr.FromInt32(80)},
				},
				InitialDelaySeconds: 1,
				PeriodSeconds:       1,
				FailureThreshold:    1,
			},
		}},
	}
	d := deployWorkload(t, client, ns, "crashy-liveness", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseCrashLoopLivenessProbe, 2*time.Minute)
}
