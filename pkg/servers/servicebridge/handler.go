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
// Package servicebridge exposes only a local trusted-operator test API.
package servicebridge

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/openkruise/agents/pkg/sandbox-manager/auditservice"
	domainerrors "github.com/openkruise/agents/pkg/sandbox-manager/errors"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
)

type Request struct {
	Target infra.ServiceTarget `json:"target"`
	Action infra.ServiceAction `json:"action"`
}
type Response struct {
	Result infra.ServiceResult `json:"result"`
	Error  string              `json:"error,omitempty"`
}

func NewHandler(m *auditservice.Manager, token string) (http.Handler, error) {
	if len(token) < 32 {
		return nil, fmt.Errorf("bridge token must be at least 32 characters")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/service-operations", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		var req Request
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := d.Decode(new(any)); err != io.EOF {
			http.Error(w, "one JSON object required", 400)
			return
		}
		result, err := m.Execute(r.Context(), req.Target, req.Action)
		w.Header().Set("Content-Type", "application/json")
		resp := Response{Result: result}
		if err != nil {
			resp.Error = err.Error()
			status := http.StatusBadGateway
			if domainerrors.GetErrCode(err) == domainerrors.ErrorBadRequest {
				status = http.StatusBadRequest
			}
			w.WriteHeader(status)
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			return
		}
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	return mux, nil
}
