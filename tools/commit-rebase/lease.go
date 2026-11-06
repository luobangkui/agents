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

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/leases"
)

const (
	// Keep operation time below lease lifetime, including cancellation cleanup.
	rebaseTimeout       = 10 * time.Minute
	rebaseLeaseTTL      = 15 * time.Minute
	leaseCleanupTimeout = 10 * time.Second
)

type leaseService interface {
	Create(context.Context, ...leases.Opt) (leases.Lease, error)
	Delete(context.Context, leases.Lease, ...leases.DeleteOpt) error
}

// withRebaseLease owns a dedicated lease even if the caller already has one.
// This avoids deleting a caller's lease or inheriting an unknown expiry.
func withRebaseLease(ctx context.Context, service leaseService, operation func(context.Context) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, rebaseTimeout)
	defer cancel()
	lease, err := service.Create(ctx, leases.WithRandomID(), leases.WithExpiration(rebaseLeaseTTL))
	if err != nil {
		return fmt.Errorf("create rebase lease: %w", err)
	}
	// Preserve namespace metadata, but do not inherit the cancelled operation.
	// SIGKILL/node loss cannot run defers; the server-side expiry bounds retention.
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), leaseCleanupTimeout)
		defer cancelCleanup()
		if cleanupErr := service.Delete(cleanupCtx, lease); cleanupErr != nil && !errdefs.IsNotFound(cleanupErr) {
			err = errors.Join(err, fmt.Errorf("release rebase lease %s (expires within %s): %w", lease.ID, rebaseLeaseTTL, cleanupErr))
		}
	}()
	return operation(leases.WithLease(ctx, lease.ID))
}
