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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/kube"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/output"
)

func newWatchCommand(configFlags *genericclioptions.ConfigFlags) *cobra.Command {
	var outputFormat string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "watch <kind>/<name>",
		Short: "Watch an ongoing rollout and classify the cause if it fails",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd, configFlags, args[0], outputFormat, timeout)
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output", "human", `output format: "human" or "json"`)
	// Default 0 means unbounded, matching `kubectl rollout status` and
	// preserving the behaviour this command always had. A bound exists
	// because a genuinely unclassified stall (a condition none of the
	// Diagnosers recognize) otherwise leaves `watch` waiting forever with no
	// output at all, which is a dangerous default inside a CI pipeline.
	// Known causes of indefinite waiting, such as a paused rollout, are
	// reported immediately by their own Diagnoser and do not depend on this
	// flag.
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "stop waiting after this long and report a timeout (0 = wait indefinitely)")
	return cmd
}

func runWatch(cmd *cobra.Command, configFlags *genericclioptions.ConfigFlags, ref, outputFormat string, timeout time.Duration) error {
	if err := validateOutputFormat(outputFormat); err != nil {
		return err
	}

	clients, err := kube.NewClients(configFlags)
	if err != nil {
		return err
	}
	namespace := clients.Namespace
	if ns := *configFlags.Namespace; ns != "" {
		namespace = ns
	}

	wl, err := kube.ResolveWorkload(cmd.Context(), clients.Clientset, namespace, ref)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	outcome, err := diagnose.Watch(ctx, diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  wl,
		Client:    clients.Clientset,
		LogTailer: kube.PreviousLogTailer{Client: clients.Clientset},
	})
	if err != nil {
		if timeout > 0 && errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("--timeout of %s reached before the rollout completed or a cause was classified: it may still be in progress, or its cause may be outside what this tool classifies today", timeout)
		}
		return err
	}

	groups := make([]output.Group, 0, len(outcome.Results))
	for _, result := range outcome.Results {
		groups = append(groups, output.Group{
			ID:         result.DiagnoserID,
			Findings:   result.Findings,
			Skipped:    result.Skipped,
			SkipReason: result.SkipReason,
		})
	}
	report := output.NewReport(groups)
	if outcome.Succeeded {
		report.Status = "succeeded"
	}
	return renderCheckReport(cmd.OutOrStdout(), report, outputFormat)
}
