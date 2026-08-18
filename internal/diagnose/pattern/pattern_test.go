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
	"testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
)

func TestMissingConfigObject_CreateContainerConfigErrorConfigMap(t *testing.T) {
	kind, name, ok := pattern.MissingConfigObject(`configmap "does-not-exist" not found`)
	if !ok || kind != "configmap" || name != "does-not-exist" {
		t.Fatalf("kind=%q name=%q ok=%v, expected configmap does-not-exist", kind, name, ok)
	}
}

func TestMissingConfigObject_CreateContainerConfigErrorSecret(t *testing.T) {
	kind, name, ok := pattern.MissingConfigObject(`secret "missing-secret" not found`)
	if !ok || kind != "secret" || name != "missing-secret" {
		t.Fatalf("kind=%q name=%q ok=%v, expected secret missing-secret", kind, name, ok)
	}
}

func TestMissingConfigObject_FailedMount(t *testing.T) {
	kind, name, ok := pattern.MissingConfigObject(`MountVolume.SetUp failed for volume "creds" : secret "missing-secret" not found`)
	if !ok || kind != "secret" || name != "missing-secret" {
		t.Fatalf("kind=%q name=%q ok=%v, expected secret missing-secret", kind, name, ok)
	}
}

func TestMissingConfigObject_UnrecognizedMessage(t *testing.T) {
	_, _, ok := pattern.MissingConfigObject(`MountVolume.SetUp failed for volume "data": rpc error`)
	if ok {
		t.Fatal("expected ok=false for a message without a missing ConfigMap or Secret")
	}
}

func TestImagePullFailure_TagNotFound(t *testing.T) {
	cause, ok := pattern.ImagePullFailure(`Failed to pull image "registry.example.com/app:v9.9.9": rpc error: code = NotFound desc = failed to pull and unpack image: failed to resolve reference: manifest unknown`)
	if !ok || cause != "tag-not-found" {
		t.Fatalf("cause=%q ok=%v, expected tag-not-found", cause, ok)
	}
}

// TestImagePullFailure_TagNotFound_RealContainerd uses the exact message
// observed on kind v0.32 / containerd 2.2 (test/e2e,
// TestWatchE2E_ImageTagInesistente): the "manifest unknown" format in the
// test above covers older containerd 1.x/CRI-O versions; this one covers
// current versions. Both remain because both occur in practice depending
// on the cluster.
func TestImagePullFailure_TagNotFound_RealContainerd(t *testing.T) {
	cause, ok := pattern.ImagePullFailure(`Failed to pull image "busybox:questo-tag-non-esiste-v99": rpc error: code = NotFound desc = failed to pull and unpack image "docker.io/library/busybox:questo-tag-non-esiste-v99": failed to resolve reference "docker.io/library/busybox:questo-tag-non-esiste-v99": docker.io/library/busybox:questo-tag-non-esiste-v99: not found`)
	if !ok || cause != "tag-not-found" {
		t.Fatalf("cause=%q ok=%v, expected tag-not-found", cause, ok)
	}
}

// TestImagePullFailure_Unauthorized uses the real message observed on kind
// for a private/nonexistent Docker Hub repository without imagePullSecrets
// (test/e2e, TestWatchE2E_CredenzialiRegistryMancanti). It also contains
// "failed to resolve reference", the same generic prefix as the
// tag-not-found message: this case made it necessary to exclude that prefix
// from the tag-not-found pattern (see the comment in pattern.go).
func TestImagePullFailure_Unauthorized(t *testing.T) {
	cause, ok := pattern.ImagePullFailure(`Failed to pull image "docker.io/kubectlsaferolloute2e/repo-privata-inesistente:v1": failed to pull and unpack image "docker.io/kubectlsaferolloute2e/repo-privata-inesistente:v1": failed to resolve reference "docker.io/kubectlsaferolloute2e/repo-privata-inesistente:v1": pull access denied, repository does not exist or may require authorization: server message: insufficient_scope: authorization failed`)
	if !ok || cause != "unauthorized" {
		t.Fatalf("cause=%q ok=%v, expected unauthorized", cause, ok)
	}
}

// TestImagePullFailure_RegistryUnreachable uses the real message observed
// on kind for a registry hostname that DNS cannot resolve (test/e2e,
// TestWatchE2E_RegistryNonRaggiungibile). Same note about the
// "failed to resolve reference" prefix as in the previous test.
func TestImagePullFailure_RegistryUnreachable(t *testing.T) {
	cause, ok := pattern.ImagePullFailure(`Failed to pull image "registry.invalid.safe-rollout-e2e.example/app:v1": failed to pull and unpack image "registry.invalid.safe-rollout-e2e.example/app:v1": failed to resolve reference "registry.invalid.safe-rollout-e2e.example/app:v1": failed to do request: Head "https://registry.invalid.safe-rollout-e2e.example/v2/app/manifests/v1": dial tcp: lookup registry.invalid.safe-rollout-e2e.example on 192.168.65.254:53: no such host`)
	if !ok || cause != "registry-unreachable" {
		t.Fatalf("cause=%q ok=%v, expected registry-unreachable", cause, ok)
	}
}

func TestImagePullFailure_UnrecognizedMessage(t *testing.T) {
	_, ok := pattern.ImagePullFailure("qualcosa che non abbiamo mai visto prima")
	if ok {
		t.Fatal("expected ok=false for a message without a known pattern")
	}
}

func TestLivenessKilling_Matches(t *testing.T) {
	if !pattern.LivenessKilling("Killing", "Container app failed liveness probe, will be restarted", "app") {
		t.Fatal("expected a match for a Killing event with a liveness probe message")
	}
}

func TestLivenessKilling_DifferentReason(t *testing.T) {
	if pattern.LivenessKilling("Unhealthy", "Liveness probe failed: HTTP probe failed with statuscode: 500", "app") {
		t.Fatal("Reason=Unhealthy must not match: it is the probe failure, not the container being killed")
	}
}

func TestLivenessKilling_DifferentContainer(t *testing.T) {
	if pattern.LivenessKilling("Killing", "Container sidecar failed liveness probe, will be restarted", "app") {
		t.Fatal("the message mentions a different container; it must not match 'app'")
	}
}

func TestFailedScheduling_InsufficientResources(t *testing.T) {
	cause, ok := pattern.FailedScheduling("0/5 nodes are available: 5 Insufficient cpu.")
	if !ok || cause != "insufficient-resources" {
		t.Fatalf("cause=%q ok=%v, expected insufficient-resources", cause, ok)
	}
}

func TestFailedScheduling_SchedulingConstraints(t *testing.T) {
	cause, ok := pattern.FailedScheduling("0/5 nodes are available: 5 node(s) didn't match Pod's node affinity/selector.")
	if !ok || cause != "scheduling-constraints" {
		t.Fatalf("cause=%q ok=%v, expected scheduling-constraints", cause, ok)
	}
}

func TestFailedScheduling_PVCUnbound(t *testing.T) {
	cause, ok := pattern.FailedScheduling("0/5 nodes are available: pod has unbound immediate PersistentVolumeClaims.")
	if !ok || cause != "unbound-pvc" {
		t.Fatalf("cause=%q ok=%v, expected unbound-pvc", cause, ok)
	}
}

func TestFailedScheduling_UnrecognizedMessage(t *testing.T) {
	_, ok := pattern.FailedScheduling("qualcosa che non abbiamo mai visto prima")
	if ok {
		t.Fatal("expected ok=false for a message without a known pattern")
	}
}

func TestQuotaExceeded_Matches(t *testing.T) {
	if !pattern.QuotaExceeded(`pods "app-abc123" is forbidden: exceeded quota: compute-quota, requested: limits.cpu=1, used: limits.cpu=4, limited: limits.cpu=4`) {
		t.Fatal("expected a match for a quota exceeded message")
	}
}

func TestQuotaExceeded_DifferentMessage(t *testing.T) {
	if pattern.QuotaExceeded(`admission webhook "policy.example.com" denied the request`) {
		t.Fatal("a webhook rejection is not a quota exceeded message")
	}
}
