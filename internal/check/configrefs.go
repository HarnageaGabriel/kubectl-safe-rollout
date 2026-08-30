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

package check

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ConfigReferencesExistCheckID is the stable identifier for this check.
const ConfigReferencesExistCheckID = "config-references-exist"

// ConfigReferencesExist verifies that every ConfigMap and Secret the pod
// template references — via envFrom, env valueFrom, or a volume source —
// actually exists, and that a reference naming a specific key finds that
// key in the object. `watch` already classifies this reactively
// (configerror-*, volumemount-*) once a Pod is stuck; this check exists
// to catch the same deterministic failure before the rollout ever starts,
// the same reasoning already applied to serviceaccount-exists.
//
// Deliberately uses one Get per referenced name rather than listing every
// ConfigMap/Secret in the namespace: a rollout only needs "get" RBAC on
// the specific names it references, not "list" access to every Secret in
// the namespace, which is a meaningfully more sensitive permission to
// require.
//
// A reference marked Optional is not reported when missing: Kubernetes
// itself treats it as non-fatal (the env var is left unset, or the volume
// is populated empty), so this check must not either.
//
// Does not follow Projected volume sources: a projected volume's
// ConfigMap/Secret/DownwardAPI/ServiceAccountToken sources are a further
// layer of indirection this check does not unwrap. Declared as a gap,
// not silently skipped.
type ConfigReferencesExist struct{}

// ID implements check.Check.
func (ConfigReferencesExist) ID() string { return ConfigReferencesExistCheckID }

// configRef is one ConfigMap/Secret reference found in the pod template.
// An empty key means a whole-object reference (envFrom, or a volume with
// no Items filter): existence of the object is all that matters.
type configRef struct {
	kind     string // "ConfigMap" or "Secret"
	name     string
	key      string
	optional bool
	source   string
}

// Run implements check.Check.
func (c ConfigReferencesExist) Run(ctx context.Context, target Target) (Result, error) {
	containers := make([]corev1.Container, 0, len(target.Workload.PodContainers())+len(target.Workload.InitContainers()))
	containers = append(containers, target.Workload.PodContainers()...)
	containers = append(containers, target.Workload.InitContainers()...)

	refs := collectContainerConfigRefs(containers)
	refs = append(refs, collectVolumeConfigRefs(target.Workload.Volumes())...)
	if len(refs) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	resolver := newConfigRefResolver(ctx, target)
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	reportedMissing := map[string]bool{}
	var findings []model.Finding
	for _, ref := range refs {
		exists, hasKey, err := resolver.resolve(ref)
		if err != nil {
			return Skip(c.ID(), err.Error()), nil
		}
		if ref.optional {
			continue
		}
		switch {
		case !exists:
			// Multiple refs (e.g. two env vars pulling different keys)
			// commonly name the same missing object: report it once, not
			// once per ref that happens to touch it.
			cacheKey := ref.kind + "/" + ref.name
			if reportedMissing[cacheKey] {
				continue
			}
			reportedMissing[cacheKey] = true
			findings = append(findings, missingObjectFinding(target, workloadRef, ref))
		case ref.key != "" && !hasKey:
			findings = append(findings, missingKeyFinding(target, workloadRef, ref))
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

func collectContainerConfigRefs(containers []corev1.Container) []configRef {
	var refs []configRef
	for _, container := range containers {
		for _, ef := range container.EnvFrom {
			if ef.ConfigMapRef != nil {
				refs = append(refs, configRef{
					kind: "ConfigMap", name: ef.ConfigMapRef.Name,
					optional: boolValue(ef.ConfigMapRef.Optional),
					source:   fmt.Sprintf("container %q envFrom", container.Name),
				})
			}
			if ef.SecretRef != nil {
				refs = append(refs, configRef{
					kind: "Secret", name: ef.SecretRef.Name,
					optional: boolValue(ef.SecretRef.Optional),
					source:   fmt.Sprintf("container %q envFrom", container.Name),
				})
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if cmkr := env.ValueFrom.ConfigMapKeyRef; cmkr != nil {
				refs = append(refs, configRef{
					kind: "ConfigMap", name: cmkr.Name, key: cmkr.Key,
					optional: boolValue(cmkr.Optional),
					source:   fmt.Sprintf("container %q env %q", container.Name, env.Name),
				})
			}
			if skr := env.ValueFrom.SecretKeyRef; skr != nil {
				refs = append(refs, configRef{
					kind: "Secret", name: skr.Name, key: skr.Key,
					optional: boolValue(skr.Optional),
					source:   fmt.Sprintf("container %q env %q", container.Name, env.Name),
				})
			}
		}
	}
	return refs
}

func collectVolumeConfigRefs(volumes []corev1.Volume) []configRef {
	var refs []configRef
	for _, v := range volumes {
		if cm := v.ConfigMap; cm != nil {
			source := fmt.Sprintf("volume %q", v.Name)
			optional := boolValue(cm.Optional)
			if len(cm.Items) == 0 {
				refs = append(refs, configRef{kind: "ConfigMap", name: cm.Name, optional: optional, source: source})
				continue
			}
			for _, item := range cm.Items {
				refs = append(refs, configRef{kind: "ConfigMap", name: cm.Name, key: item.Key, optional: optional, source: source})
			}
		}
		if s := v.Secret; s != nil {
			source := fmt.Sprintf("volume %q", v.Name)
			optional := boolValue(s.Optional)
			if len(s.Items) == 0 {
				refs = append(refs, configRef{kind: "Secret", name: s.SecretName, optional: optional, source: source})
				continue
			}
			for _, item := range s.Items {
				refs = append(refs, configRef{kind: "Secret", name: s.SecretName, key: item.Key, optional: optional, source: source})
			}
		}
	}
	return refs
}

func boolValue(b *bool) bool { return b != nil && *b }

// configRefResolver memoizes Get calls: multiple references commonly name
// the same ConfigMap or Secret (for example, several env vars pulled from
// one shared ConfigMap), and this check must not issue a redundant Get
// per reference.
type configRefResolver struct {
	ctx        context.Context
	target     Target
	configMaps map[string]*corev1.ConfigMap
	secrets    map[string]*corev1.Secret
	missing    map[string]bool
}

func newConfigRefResolver(ctx context.Context, target Target) *configRefResolver {
	return &configRefResolver{
		ctx: ctx, target: target,
		configMaps: map[string]*corev1.ConfigMap{},
		secrets:    map[string]*corev1.Secret{},
		missing:    map[string]bool{},
	}
}

// resolve reports whether ref's object exists and, if ref names a
// specific key, whether that key is present in it.
func (r *configRefResolver) resolve(ref configRef) (exists, hasKey bool, err error) {
	cacheKey := ref.kind + "/" + ref.name
	if r.missing[cacheKey] {
		return false, false, nil
	}
	switch ref.kind {
	case "ConfigMap":
		cm, ok := r.configMaps[ref.name]
		if !ok {
			fetched, getErr := r.target.Client.CoreV1().ConfigMaps(r.target.Namespace).Get(r.ctx, ref.name, metav1.GetOptions{})
			switch {
			case getErr == nil:
				cm = fetched
				r.configMaps[ref.name] = cm
			case apierrors.IsNotFound(getErr):
				r.missing[cacheKey] = true
				return false, false, nil
			default:
				return false, false, fmt.Errorf("failed to read ConfigMap %q: %w", ref.name, getErr)
			}
		}
		if ref.key == "" {
			return true, false, nil
		}
		_, inData := cm.Data[ref.key]
		_, inBinary := cm.BinaryData[ref.key]
		return true, inData || inBinary, nil
	case "Secret":
		s, ok := r.secrets[ref.name]
		if !ok {
			fetched, getErr := r.target.Client.CoreV1().Secrets(r.target.Namespace).Get(r.ctx, ref.name, metav1.GetOptions{})
			switch {
			case getErr == nil:
				s = fetched
				r.secrets[ref.name] = s
			case apierrors.IsNotFound(getErr):
				r.missing[cacheKey] = true
				return false, false, nil
			default:
				return false, false, fmt.Errorf("failed to read Secret %q: %w", ref.name, getErr)
			}
		}
		if ref.key == "" {
			return true, false, nil
		}
		_, inData := s.Data[ref.key]
		return true, inData, nil
	default:
		return false, false, nil
	}
}

func missingObjectFinding(target Target, workloadRef string, ref configRef) model.Finding {
	return model.Finding{
		CheckID:  ConfigReferencesExistCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%s references %s %q in %s, which does not exist in namespace %q: the kubelet cannot start this container",
			workloadRef, ref.kind, ref.name, ref.source, target.Namespace,
		),
		Evidence: []string{fmt.Sprintf("%s=%s source=%s", ref.kind, ref.name, ref.source)},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("create %s %q in namespace %q, or correct the reference in %s", ref.kind, ref.name, target.Namespace, ref.source),
			Commands:         []string{fmt.Sprintf("kubectl get %s %s -n %s", strings.ToLower(ref.kind), ref.name, target.Namespace)},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{Kind: ref.kind, Namespace: target.Namespace, Name: ref.name},
	}
}

func missingKeyFinding(target Target, workloadRef string, ref configRef) model.Finding {
	return model.Finding{
		CheckID:  ConfigReferencesExistCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%s references key %q of %s %q in %s, which does not exist: the kubelet cannot start this container",
			workloadRef, ref.key, ref.kind, ref.name, ref.source,
		),
		Evidence: []string{fmt.Sprintf("%s=%s key=%s source=%s", ref.kind, ref.name, ref.key, ref.source)},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("add key %q to %s %q, or correct the key referenced in %s", ref.key, ref.kind, ref.name, ref.source),
			Commands:         []string{fmt.Sprintf("kubectl get %s %s -n %s -o yaml", strings.ToLower(ref.kind), ref.name, target.Namespace)},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{Kind: ref.kind, Namespace: target.Namespace, Name: ref.name},
	}
}
