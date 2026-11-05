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
package katabridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	lifecycle "github.com/openkruise/agents/katalab/pkg/infra"
	"github.com/openkruise/agents/katalab/pkg/manager"
	"github.com/openkruise/agents/pkg/sandbox-manager/auditservice"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
	"github.com/openkruise/agents/pkg/servers/servicebridge"
)

func New(b *manager.Manager, services *auditservice.Manager, token string) (http.Handler, error) {
	service, e := servicebridge.NewHandler(services, token)
	if e != nil {
		return nil, e
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/service-operations", service)
	mux.Handle("/healthz", service)
	wrap := func(next func(context.Context, json.RawMessage) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 128*1024))
			var result any
			if e == nil {
				result, e = next(r.Context(), raw)
			}
			w.Header().Set("Content-Type", "application/json")
			v := map[string]any{"data": result}
			if e != nil {
				w.WriteHeader(502)
				v["error"] = e.Error()
			}
			if e = json.NewEncoder(w).Encode(v); e != nil {
				return
			}
		}
	}
	decode := func(raw []byte, v any) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if e := d.Decode(v); e != nil {
			return e
		}
		if e := d.Decode(new(any)); e != io.EOF {
			return fmt.Errorf("one object required")
		}
		return nil
	}
	mux.HandleFunc("POST /v1/runtime/create", wrap(func(ctx context.Context, raw json.RawMessage) (any, error) {
		var s lifecycle.CreateSpec
		if e := decode(raw, &s); e != nil {
			return nil, e
		}
		return b.Prepare(ctx, s)
	}))
	mux.HandleFunc("POST /v1/runtime/inspect", wrap(func(ctx context.Context, raw json.RawMessage) (any, error) {
		var s struct {
			ID string `json:"id"`
		}
		if e := decode(raw, &s); e != nil {
			return nil, e
		}
		return b.State(ctx, s.ID)
	}))
	mux.HandleFunc("POST /v1/runtime/reclaim", wrap(func(ctx context.Context, raw json.RawMessage) (any, error) {
		var s struct {
			ID string `json:"id"`
		}
		if e := decode(raw, &s); e != nil {
			return nil, e
		}
		clean, done := context.WithTimeout(context.WithoutCancel(ctx), 38*time.Second)
		defer done()
		e := b.Reclaim(clean, s.ID)
		return map[string]bool{"reclaimed": e == nil}, e
	}))
	mux.HandleFunc("POST /v1/runtime/stop-receipt", wrap(func(ctx context.Context, raw json.RawMessage) (any, error) {
		var s struct {
			Target  infra.ServiceTarget `json:"target"`
			Service string              `json:"service"`
		}
		if e := decode(raw, &s); e != nil {
			return nil, e
		}
		return b.RecoverStop(ctx, s.Target, s.Service)
	}))
	return mux, nil
}
