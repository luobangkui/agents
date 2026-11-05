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
package servicebridge

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openkruise/agents/pkg/sandbox-manager/auditservice"
)

func TestBridgeRejectsInvalidRequestsBeforeBackend(t *testing.T) {
	token := strings.Repeat("b", 32)
	h, err := NewHandler(auditservice.New(nil), token)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, token, body string
		want              int
	}{
		{"missing-auth", "", `{}`, 401},
		{"strict-json", token, `{"target":{},"unknown":1}`, 400},
		{"missing-binding", token, `{"target":{},"action":{"kind":"exec","service":"main","timeout_seconds":1}}`, 400},
		{"wrong-service", token, `{"target":{"execution_id":"e","owner_id":"o","instance_id":"p"},"action":{"kind":"exec","service":"runtime","timeout_seconds":1}}`, 400},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/service-operations", strings.NewReader(c.body))
			r.Header.Set("Authorization", "Bearer "+c.token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
