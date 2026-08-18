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

// Command kubectl-safe_rollout is the entry point for the krew plugin
// "safe-rollout". The binary name follows the krew convention for
// multi-word commands (underscore instead of hyphen); help and messages
// still present it as "kubectl safe-rollout".
package main

import (
	"errors"
	"fmt"
	"os"
)

// errHighSeverity is a sentinel error: it tells main to exit with a
// non-zero code because high-severity findings were found, not because
// execution failed. The distinction matters because in that case the
// output (already printed by the command) is the message, and repeating
// it as an "error" on stderr would be misleading.
var errHighSeverity = errors.New("high-severity findings found")

func main() {
	err := newRootCommand().Execute()
	if err == nil {
		return
	}
	if !errors.Is(err, errHighSeverity) {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}
