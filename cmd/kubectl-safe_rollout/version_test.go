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
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestVersionCommandWritesBuildInformationToConfiguredOutput(t *testing.T) {
	var output bytes.Buffer
	cmd := newVersionCommand()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected version command to succeed: %v", err)
	}

	got := output.String()
	if !strings.Contains(got, version) {
		t.Errorf("expected output to contain default version %q, got %q", version, got)
	}
	if !strings.Contains(got, runtime.GOOS) {
		t.Errorf("expected output to contain current operating system %q, got %q", runtime.GOOS, got)
	}
}
