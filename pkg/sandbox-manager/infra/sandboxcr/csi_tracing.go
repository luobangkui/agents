/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sandboxcr

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openkruise/agents/api/v1alpha1"
	"github.com/openkruise/agents/pkg/tracing"
	"github.com/openkruise/agents/pkg/utils/runtime"
	"github.com/openkruise/agents/pkg/utils/runtime/config"
)

type dynamicMountCleanupIncompleteError struct {
	err error
}

func (e *dynamicMountCleanupIncompleteError) Error() string {
	return fmt.Sprintf("dynamic mount cleanup remains incomplete: %v", e.err)
}

func (e *dynamicMountCleanupIncompleteError) Unwrap() error {
	return e.err
}

// traceCSIMounts wraps runtime.ProcessCSIMounts in a manager child span that
// records the volume count and driver list, returning the mount duration and
// error so each caller (claim, clone) keeps its own metrics reporting and
// error wrapping. rtOpts is forwarded to the runtime transport so a
// TLS-capable sandbox mounts its volumes over HTTPS; empty keeps plaintext.
func traceCSIMounts(ctx context.Context, kubeClient client.Client, sbx *v1alpha1.Sandbox, opts config.CSIMountOptions, rtOpts ...runtime.Option) (time.Duration, error) {
	ctx, span := tracing.StartManagerSpan(ctx, tracing.SpanInfraProcessCSIMounts)
	// Project the mount configs onto the driver-name list accepted by
	// attribute.StringSlice (which copies the slice internally).
	drivers := make([]string, 0, len(opts.MountOptionList)+len(opts.StagedMountOptionList))
	for _, m := range opts.MountOptionList {
		drivers = append(drivers, m.Driver)
	}
	for _, m := range opts.StagedMountOptionList {
		drivers = append(drivers, m.Plan.Driver)
	}
	span.SetAttributes(
		attribute.Int(tracing.AttrCSIVolumeCount, len(drivers)),
		attribute.StringSlice(tracing.AttrCSIDrivers, drivers),
	)
	stagedDuration, err := processKubeletAnchorMounts(ctx, kubeClient, sbx, opts)
	if err == nil {
		var directDuration time.Duration
		directDuration, err = runtime.ProcessCSIMounts(ctx, sbx, opts, rtOpts...)
		stagedDuration += directDuration
	}
	if err != nil {
		// A claim is atomic from the caller's perspective. Direct unpublish is
		// idempotent, so rolling back the full resolved list safely covers both
		// successful siblings and the mount that reported the failure.
		if len(opts.MountOptionList) > 0 {
			if _, cleanupErr := runtime.ProcessCSIUnmounts(ctx, sbx, opts, rtOpts...); cleanupErr != nil {
				err = errors.Join(err, &dynamicMountCleanupIncompleteError{
					err: fmt.Errorf("rollback direct CSI mounts: %w", cleanupErr),
				})
			}
		}
		if len(opts.StagedMountOptionList) > 0 {
			if cleanupErr := cleanupKubeletAnchors(ctx, kubeClient, sbx); cleanupErr != nil {
				err = errors.Join(err, &dynamicMountCleanupIncompleteError{
					err: fmt.Errorf("rollback staged CSI mounts: %w", cleanupErr),
				})
			}
		}
	}
	tracing.EndSpan(ctx, span, err)
	return stagedDuration, err
}
