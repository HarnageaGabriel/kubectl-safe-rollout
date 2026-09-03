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

package pattern_test

import (
	"strings"
	"testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
)

func degenerateMessages() []string {
	return []string{
		"",
		" ",
		strings.Repeat("x", 64*1024),
		":",
		"/",
		" : ",
		string([]byte{0xff, 0xfe, 0x80}),
	}
}

func assertKnownCause(t *testing.T, cause string, ok bool, known map[string]struct{}) {
	t.Helper()
	if !ok {
		return
	}
	if _, exists := known[cause]; !exists {
		t.Fatalf("classified input with unknown cause %q", cause)
	}
}

func FuzzMissingConfigObject(f *testing.F) {
	for _, message := range []string{
		`configmap "does-not-exist" not found`,
		`secret "missing-secret" not found`,
		`MountVolume.SetUp failed for volume "creds" : secret "missing-secret" not found`,
		`MountVolume.SetUp failed for volume "data": rpc error`,
		`configmap "name" not foundling`,
	} {
		f.Add(message)
	}
	for _, message := range degenerateMessages() {
		f.Add(message)
	}

	f.Fuzz(func(t *testing.T, message string) {
		kind, _, ok := pattern.MissingConfigObject(message)
		assertKnownCause(t, kind, ok, map[string]struct{}{
			"configmap": {},
			"secret":    {},
		})
	})
}

func FuzzImagePullFailure(f *testing.F) {
	for _, message := range []string{
		`Failed to pull image "registry.example.com/app:v9.9.9": rpc error: code = NotFound desc = failed to pull and unpack image: failed to resolve reference: manifest unknown`,
		`Failed to pull image "busybox:this-tag-does-not-exist-v99": rpc error: code = NotFound desc = failed to pull and unpack image "docker.io/library/busybox:this-tag-does-not-exist-v99": failed to resolve reference "docker.io/library/busybox:this-tag-does-not-exist-v99": docker.io/library/busybox:this-tag-does-not-exist-v99: not found`,
		`Failed to pull image "docker.io/kubectlsaferolloute2e/nonexistent-private-repo:v1": failed to pull and unpack image "docker.io/kubectlsaferolloute2e/nonexistent-private-repo:v1": failed to resolve reference "docker.io/kubectlsaferolloute2e/nonexistent-private-repo:v1": pull access denied, repository does not exist or may require authorization: server message: insufficient_scope: authorization failed`,
		`Failed to pull image "registry.invalid.safe-rollout-e2e.example/app:v1": failed to pull and unpack image "registry.invalid.safe-rollout-e2e.example/app:v1": failed to resolve reference "registry.invalid.safe-rollout-e2e.example/app:v1": failed to do request: Head "https://registry.invalid.safe-rollout-e2e.example/v2/app/manifests/v1": dial tcp: lookup registry.invalid.safe-rollout-e2e.example on 192.168.65.254:53: no such host`,
		"something we have never seen before",
		"manifest unknownness",
	} {
		f.Add(message)
	}
	for _, message := range degenerateMessages() {
		f.Add(message)
	}

	f.Fuzz(func(t *testing.T, message string) {
		cause, ok := pattern.ImagePullFailure(message)
		assertKnownCause(t, cause, ok, map[string]struct{}{
			"tag-not-found":        {},
			"unauthorized":         {},
			"registry-unreachable": {},
		})
	})
}

func FuzzLivenessKilling(f *testing.F) {
	for _, seed := range [][3]string{
		{"Killing", "Container app failed liveness probe, will be restarted", "app"},
		{"Unhealthy", "Liveness probe failed: HTTP probe failed with statuscode: 500", "app"},
		{"Killing", "Container sidecar failed liveness probe, will be restarted", "app"},
		{"Killing", "Container app failed liveness probesomething", "app"},
	} {
		f.Add(seed[0], seed[1], seed[2])
	}
	for _, input := range degenerateMessages() {
		f.Add(input, input, input)
	}

	f.Fuzz(func(t *testing.T, reason, message, container string) {
		pattern.LivenessKilling(reason, message, container)
	})
}

func FuzzReadinessFailure(f *testing.F) {
	for _, seed := range [][2]string{
		{"Unhealthy", "Readiness probe failed: "},
		{"Unhealthy", "Readiness probe failed: HTTP probe failed with statuscode: 500"},
		{"Unhealthy", "Liveness probe failed: HTTP probe failed with statuscode: 500"},
		{"Unhealthy", "readiness probe failedness"},
	} {
		f.Add(seed[0], seed[1])
	}
	for _, input := range degenerateMessages() {
		f.Add(input, input)
	}

	f.Fuzz(func(t *testing.T, reason, message string) {
		pattern.ReadinessFailure(reason, message)
	})
}

func FuzzFailedScheduling(f *testing.F) {
	for _, message := range []string{
		"0/5 nodes are available: 5 Insufficient cpu.",
		"0/5 nodes are available: 5 node(s) didn't match Pod's node affinity/selector.",
		"0/5 nodes are available: pod has unbound immediate PersistentVolumeClaims.",
		"something we have never seen before",
		"insufficient cpuish",
	} {
		f.Add(message)
	}
	for _, message := range degenerateMessages() {
		f.Add(message)
	}

	f.Fuzz(func(t *testing.T, message string) {
		cause, ok := pattern.FailedScheduling(message)
		assertKnownCause(t, cause, ok, map[string]struct{}{
			"unbound-pvc":            {},
			"insufficient-resources": {},
			"scheduling-constraints": {},
		})
	})
}

func FuzzQuotaExceeded(f *testing.F) {
	for _, message := range []string{
		`pods "app-abc123" is forbidden: exceeded quota: compute-quota, requested: limits.cpu=1, used: limits.cpu=4, limited: limits.cpu=4`,
		`admission webhook "policy.example.com" denied the request`,
		"exceeded quotafoo",
	} {
		f.Add(message)
	}
	for _, message := range degenerateMessages() {
		f.Add(message)
	}

	f.Fuzz(func(t *testing.T, message string) {
		pattern.QuotaExceeded(message)
	})
}
