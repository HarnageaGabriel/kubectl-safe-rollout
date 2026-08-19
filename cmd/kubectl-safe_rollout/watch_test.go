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

package main

import (
	"testing"
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// The default must stay 0 (unbounded): watch's original behaviour, and the
// one every existing caller relies on. A nonzero default would silently
// change what already-written automation does.
func TestNewWatchCommand_TimeoutDefaultsToUnbounded(t *testing.T) {
	configFlags := genericclioptions.NewConfigFlags(true)
	cmd := newWatchCommand(configFlags)

	flag := cmd.Flags().Lookup("timeout")
	if flag == nil {
		t.Fatal("expected a --timeout flag to be registered")
	}
	if flag.DefValue != "0s" {
		t.Errorf("--timeout default = %q, expected \"0s\" (unbounded, preserving prior behavior)", flag.DefValue)
	}
}

func TestNewWatchCommand_TimeoutParsesDurations(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"30s", 30 * time.Second},
		{"1h30m", 90 * time.Minute},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			configFlags := genericclioptions.NewConfigFlags(true)
			cmd := newWatchCommand(configFlags)
			if err := cmd.Flags().Set("timeout", tc.arg); err != nil {
				t.Fatalf("Set(%q): %v", tc.arg, err)
			}
			got, err := cmd.Flags().GetDuration("timeout")
			if err != nil {
				t.Fatalf("GetDuration: %v", err)
			}
			if got != tc.want {
				t.Errorf("--timeout=%q parsed as %v, expected %v", tc.arg, got, tc.want)
			}
		})
	}
}

func TestNewWatchCommand_TimeoutRejectsGarbage(t *testing.T) {
	configFlags := genericclioptions.NewConfigFlags(true)
	cmd := newWatchCommand(configFlags)
	if err := cmd.Flags().Set("timeout", "not-a-duration"); err == nil {
		t.Fatal("expected an error setting --timeout to a non-duration value, got nil")
	}
}
