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
package e2b

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSandboxServiceRoutesRequireAuthentication(t *testing.T) {
	sc, _, done := Setup(t)
	defer done()
	for _, prefix := range []string{"", "/kruise/api"} {
		for _, spec := range []struct{ method, path string }{{"GET", "/services"}, {"POST", "/services/main/exec"}, {"GET", "/services/main/files?path=/app/result.json"}, {"POST", "/services/main/stop"}} {
			t.Run(prefix+spec.path, func(t *testing.T) {
				w := httptest.NewRecorder()
				sc.mux.ServeHTTP(w, httptest.NewRequest(spec.method, prefix+"/sandboxes/test"+spec.path, strings.NewReader(`{}`)))
				require.Equal(t, http.StatusUnauthorized, w.Code)
			})
		}
	}
}
func TestSandboxServiceStrictRequest(t *testing.T) {
	for _, body := range []string{`{"podUID":"p","user":"root"}`, `{"podUID":"p","container":"other"}`, `{} {}`, `{"namespace":"other"}`, strings.Repeat(" ", 128*1024) + `{}`} {
		_, err := serviceRequest(httptest.NewRequest("POST", "/", strings.NewReader(body)))
		require.NotNil(t, err)
		require.Equal(t, 400, err.Code)
	}
}
