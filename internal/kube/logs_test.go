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

package kube_test

import (
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/kube"
)

func TestPreviousLogTailer_UsesLogSubresource(t *testing.T) {
	client := fake.NewSimpleClientset()
	tailer := kube.PreviousLogTailer{Client: client}

	logs, err := tailer.PreviousLogTail(t.Context(), "default", "app-1", "app", 3)
	if err != nil {
		t.Fatalf("PreviousLogTail: %v", err)
	}
	if logs != "fake logs" {
		t.Fatalf("logs = %q, expected output from fake REST client", logs)
	}
	actions := client.Actions()
	if len(actions) != 1 || actions[0].GetVerb() != "get" || actions[0].GetSubresource() != "log" {
		t.Fatalf("unexpected client action: %+v", actions)
	}
}
