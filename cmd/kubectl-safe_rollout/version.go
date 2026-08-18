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
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// These defaults identify a plain go build as local, so a dev binary cannot be
// mistaken for a release artifact when triaging a bug report.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), `kubectl safe-rollout %s
commit:    %s
built:     %s
go:        %s
platform:  %s/%s
`, version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
