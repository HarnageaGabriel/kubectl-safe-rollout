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

package kube

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// ResolveWorkload interprets a reference in the form "<kind>/<name>"
// (the same syntax accepted by `kubectl get`) and retrieves the corresponding
// live object. Only Deployment and StatefulSet are supported: other kinds
// return an explicit error instead of silently providing partial behavior,
// so users know they must wait for future support instead of trusting an
// incomplete result.
func ResolveWorkload(ctx context.Context, clientset kubernetes.Interface, namespace, ref string) (workload.Workload, error) {
	kind, name, err := splitRef(ref)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(kind) {
	case "deployment", "deployments", "deploy", "deploy.apps", "deployment.apps":
		d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("reading Deployment %s/%s: %w", namespace, name, err)
		}
		return workload.FromDeployment(d), nil
	case "statefulset", "statefulsets", "sts", "sts.apps", "statefulset.apps":
		s, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("reading StatefulSet %s/%s: %w", namespace, name, err)
		}
		return workload.FromStatefulSet(s), nil
	default:
		return nil, fmt.Errorf("kind %q is not supported in this version (only Deployment and StatefulSet are supported)", kind)
	}
}

func splitRef(ref string) (kind, name string, err error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid reference %q, expected <kind>/<name> (e.g. deployment/api)", ref)
	}
	return parts[0], parts[1], nil
}
