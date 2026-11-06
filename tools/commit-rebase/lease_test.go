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
	"testing"
	"time"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/namespaces"
)

type fakeLeaseService struct {
	lease                leases.Lease
	createErr, deleteErr error
	deleted              int
	cleanupErr           error
	cleanupNamespace     string
	cleanupDeadline      time.Time
}

func (f *fakeLeaseService) Create(ctx context.Context, opts ...leases.Opt) (leases.Lease, error) {
	if f.createErr != nil {
		return leases.Lease{}, f.createErr
	}
	for _, opt := range opts {
		if err := opt(&f.lease); err != nil {
			return leases.Lease{}, err
		}
	}
	return f.lease, nil
}
func (f *fakeLeaseService) Delete(ctx context.Context, l leases.Lease, _ ...leases.DeleteOpt) error {
	f.deleted++
	f.cleanupErr = ctx.Err()
	f.cleanupNamespace, _ = namespaces.Namespace(ctx)
	f.cleanupDeadline, _ = ctx.Deadline()
	if l.ID != f.lease.ID {
		panic("wrong lease deleted")
	}
	return f.deleteErr
}
func TestRebaseLeaseLifecycle(t *testing.T) {
	opErr := errors.New("write failed")
	cleanupErr := errors.New("daemon unavailable")
	for _, tc := range []struct {
		name                        string
		cancel, panics              bool
		createErr, opErr, deleteErr error
		wantDeletes                 int
	}{
		{name: "success", wantDeletes: 1},
		{name: "write_failure", opErr: opErr, wantDeletes: 1},
		{name: "cancelled_signal", cancel: true, opErr: context.Canceled, wantDeletes: 1},
		{name: "panic", panics: true, wantDeletes: 1},
		{name: "create_failure", createErr: opErr},
		{name: "cleanup_failure", deleteErr: cleanupErr, wantDeletes: 1},
		{name: "both_failures", opErr: opErr, deleteErr: cleanupErr, wantDeletes: 1},
		{name: "already_expired", deleteErr: errdefs.ErrNotFound, wantDeletes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(namespaces.WithNamespace(context.Background(), "test-ns"))
			defer cancel()
			ctx = leases.WithLease(ctx, "caller-owned")
			f := &fakeLeaseService{createErr: tc.createErr, deleteErr: tc.deleteErr}
			var err error
			var recovered any
			called := false
			func() {
				defer func() { recovered = recover() }()
				err = withRebaseLease(ctx, f, func(op context.Context) error {
					called = true
					id, ok := leases.FromContext(op)
					if !ok || id == "caller-owned" || id != f.lease.ID {
						t.Error("operation not bound to dedicated lease")
					}
					deadline, ok := op.Deadline()
					if !ok || time.Until(deadline) > rebaseTimeout {
						t.Error("missing operation bound")
					}
					expires, e := time.Parse(time.RFC3339, f.lease.Labels["containerd.io/gc.expire"])
					if e != nil || !expires.After(deadline) || time.Until(expires) > rebaseLeaseTTL {
						t.Error("invalid finite expiry")
					}
					if tc.cancel {
						cancel()
					}
					if tc.panics {
						panic("panic path")
					}
					return tc.opErr
				})
			}()
			if (recovered != nil) != tc.panics {
				t.Fatalf("panic=%v", recovered)
			}
			if called != (tc.createErr == nil) {
				t.Error("unexpected operation invocation")
			}
			if f.deleted != tc.wantDeletes {
				t.Fatalf("deletes=%d", f.deleted)
			}
			if f.deleted > 0 && (f.cleanupErr != nil || f.cleanupNamespace != "test-ns" || f.cleanupDeadline.IsZero()) {
				t.Error("cleanup inherited cancellation or lost namespace/deadline")
			}
			if !tc.panics {
				for _, want := range []error{tc.createErr, tc.opErr, tc.deleteErr} {
					if want != nil && !errdefs.IsNotFound(want) && !errors.Is(err, want) {
						t.Errorf("lost error %v: %v", want, err)
					}
				}
				if tc.opErr == nil && tc.createErr == nil && (tc.deleteErr == nil || errdefs.IsNotFound(tc.deleteErr)) && err != nil {
					t.Errorf("unexpected error %v", err)
				}
			}
		})
	}
}
