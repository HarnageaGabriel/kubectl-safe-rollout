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
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// PreviousLogTailer retrieves a limited tail of the previous container's
// logs. It implements diagnose.LogTailer without creating a reverse
// dependency from kube to diagnose.
type PreviousLogTailer struct {
	Client kubernetes.Interface
}

// PreviousLogTail retrieves no more than the requested number of lines and
// also limits the bytes read, to prevent a non-compliant runtime from
// producing unlimited output.
func (t PreviousLogTailer) PreviousLogTail(ctx context.Context, namespace, pod, container string, lines int64) (string, error) {
	req := t.Client.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
		Previous:  true,
		TailLines: &lines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("opening previous logs for Pod/%s container %s: %w", pod, container, err)
	}
	defer func() {
		_ = stream.Close()
	}()

	const maxLogBytes = 16 * 1024
	b, err := io.ReadAll(io.LimitReader(stream, maxLogBytes))
	if err != nil {
		return "", fmt.Errorf("reading previous logs for Pod/%s container %s: %w", pod, container, err)
	}
	return string(b), nil
}
