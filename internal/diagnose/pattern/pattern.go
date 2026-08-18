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

// Package pattern isolates all free-text parsing (Event.Message from the
// kubelet, scheduler, and container runtime) used by internal/diagnose. It
// is deliberately a single point: these messages are not a stable
// Kubernetes API, they change across versions and runtimes (containerd vs
// CRI-O produce different text for the same pull error), and scattering
// string matching through classification logic would make it impossible to
// understand, update, or test in one place what this project recognizes.
//
// Every function here distinguishes structured signals from textual ones:
// where Kubernetes exposes a stable field (Reason on an Event or
// ContainerState, Phase on a PersistentVolumeClaim), Diagnosers use it
// directly and do not go through this package. Only signals with no
// structured alternative live here. Every function returns ok=false when
// no known pattern matches: the caller must treat it as an undetermined
// cause, never choose by guessing.
package pattern

import (
	"regexp"
	"strings"
)

var missingConfigObjectPattern = regexp.MustCompile(`(?i)\b(configmap|secret)\s+"([^"]+)"\s+not found\b`)

// MissingConfigObject extracts a missing ConfigMap or Secret named in a
// kubelet message. Kubernetes exposes the containing failure category as a
// structured Reason, but the referenced object exists only in free text.
func MissingConfigObject(message string) (kind, name string, ok bool) {
	match := missingConfigObjectPattern.FindStringSubmatch(message)
	if match == nil {
		return "", "", false
	}
	return strings.ToLower(match[1]), match[2], true
}

// ImagePullFailure classifies the message of a Reason=="Failed" event
// generated while pulling an image (typically alongside a container in
// Waiting state with Reason "ImagePullBackOff" or "ErrImagePull", which
// the caller has already checked separately through the structured field).
func ImagePullFailure(message string) (cause string, ok bool) {
	m := strings.ToLower(message)
	switch {
	// "repository does not exist" is deliberately excluded from this
	// case: it is also part of Docker's classic message for a pull rejected
	// due to authorization ("pull access denied for X, repository does not
	// exist or may require 'docker login'"), not only for a genuinely
	// missing image. "failed to resolve reference" is excluded for the
	// same reason: it is a generic prefix containerd uses for ANY resolution
	// failure (verified on kind v0.32/containerd 2.2: it appears unchanged in
	// messages for a missing tag, missing credentials, and an unreachable
	// registry), not specifically for "not found". The ": not found" suffix
	// is genuinely specific: in the real containerd 2.x message it appears
	// only when the tag/digest does not exist
	// ("...: docker.io/library/busybox:tag: not found"). "manifest
	// unknown"/"manifest for ... not found" cover containerd 1.x and older
	// CRI-O versions.
	case containsAny(m, ": not found", "manifest unknown", "manifest for", "name unknown"):
		return "tag-not-found", true
	case containsAny(m, "unauthorized", "pull access denied", "insufficient_scope", "authentication required", "does not exist or may require", "401", "403"):
		return "unauthorized", true
	case containsAny(m, "no such host", "i/o timeout", "connection refused", "context deadline exceeded", "tls handshake timeout", "network is unreachable", "no route to host"):
		return "registry-unreachable", true
	default:
		return "", false
	}
}

// LivenessKilling recognizes the event the kubelet generates when it
// terminates a container after repeated liveness probe failures.
// Reason=="Killing" is a stable value (an internal kubelet constant not
// expected to change: kubectl describe and many observability tools already
// rely on it). The associated message is free text ("Container %s failed
// liveness probe, will be restarted"): the combination is the most reliable
// signal available without importing k8s.io/kubernetes, which is not a
// dependency intended for external use.
func LivenessKilling(reason, message, container string) bool {
	if reason != "Killing" {
		return false
	}
	m := strings.ToLower(message)
	if !strings.Contains(m, "liveness probe") {
		return false
	}
	return container == "" || strings.Contains(message, container)
}

// ReadinessFailure recognizes the event the kubelet generates when a
// readiness probe fails. Readiness and liveness probe failures both produce
// Reason=="Unhealthy" events, so Reason alone cannot distinguish them; only
// the free-text message identifies which probe failed.
func ReadinessFailure(reason, message string) bool {
	return reason == "Unhealthy" && strings.Contains(strings.ToLower(message), "readiness probe failed")
}

// FailedScheduling classifies the message of a Reason=="FailedScheduling"
// event generated by the scheduler for a Pending pod.
func FailedScheduling(message string) (cause string, ok bool) {
	m := strings.ToLower(message)
	switch {
	case containsAny(m, "unbound immediate persistentvolumeclaims", "unbound persistentvolumeclaims", "persistentvolumeclaim not found", "waiting for first consumer"):
		return "unbound-pvc", true
	case containsAny(m, "insufficient cpu", "insufficient memory", "insufficient ephemeral-storage", "insufficient pods"):
		return "insufficient-resources", true
	case containsAny(m, "didn't match pod's node affinity", "didn't match pod's node selector", "node(s) had taint", "didn't tolerate", "node(s) didn't match node selector", "node(s) had untolerated taint"):
		return "scheduling-constraints", true
	default:
		return "", false
	}
}

// QuotaExceeded recognizes the message of a Reason=="FailedCreate" event
// on a ReplicaSet blocked by the ResourceQuota admission plugin. The
// "exceeded quota:" prefix is generated by k8s.io/apiserver/plugin/pkg/
// admission/resourcequota and has been stable across many major versions,
// unlike the scheduler/kubelet messages above: it is still isolated here
// with the others, not in Diagnoser logic, for the same maintainability
// reason.
func QuotaExceeded(message string) bool {
	return strings.Contains(strings.ToLower(message), "exceeded quota")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
