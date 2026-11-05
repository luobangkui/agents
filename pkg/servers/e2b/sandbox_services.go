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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	managererrors "github.com/openkruise/agents/pkg/sandbox-manager/errors"
	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
	"github.com/openkruise/agents/pkg/servers/web"
)

// These are explicit Sandbox API extensions, registered identically on native
// and customized paths. They do not change the E2B command/envd protocol.
func (sc *Controller) registerSandboxServiceRoutes() {
	RegisterE2BRoute(sc.mux, "GET", "/sandboxes/{sandboxID}/services", sc.DescribeSandboxServices, sc.CheckApiKey)
	RegisterE2BRoute(sc.mux, "POST", "/sandboxes/{sandboxID}/services/{service}/exec", sc.ExecSandboxService, sc.CheckApiKey)
	RegisterE2BRoute(sc.mux, "GET", "/sandboxes/{sandboxID}/services/{service}/files", sc.DownloadSandboxServiceFile, sc.CheckApiKey)
	RegisterE2BRoute(sc.mux, "POST", "/sandboxes/{sandboxID}/services/{service}/stop", sc.StopSandboxService, sc.CheckApiKey)
}

type sandboxServiceRequest struct {
	PodUID         string   `json:"podUID"`
	Args           []string `json:"args,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

func serviceRequest(r *http.Request) (sandboxServiceRequest, *web.ApiError) {
	var v sandboxServiceRequest
	raw, err := io.ReadAll(io.LimitReader(r.Body, 128*1024+1))
	if err != nil || len(raw) > 128*1024 {
		return v, &web.ApiError{Code: 400, Message: "service request exceeds 128 KiB or cannot be read"}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return v, &web.ApiError{Code: 400, Message: "Invalid service request: " + err.Error()}
	}
	if d.Decode(new(any)) != io.EOF {
		return v, &web.ApiError{Code: 400, Message: "one service request object required"}
	}
	if v.TimeoutSeconds == 0 {
		v.TimeoutSeconds = 30
	}
	return v, nil
}
func sandboxServiceError(err error) *web.ApiError {
	code := http.StatusBadGateway
	switch managererrors.GetErrCode(err) {
	case managererrors.ErrorBadRequest:
		code = 400
	case managererrors.ErrorNotAllowed:
		code = 403
	case managererrors.ErrorNotFound:
		code = 404
	case managererrors.ErrorConflict:
		code = 409
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		code = 504
	}
	return &web.ApiError{Code: code, Message: err.Error(), Headers: map[string]string{"Should-Retry": "false"}}
}
func (sc *Controller) DescribeSandboxServices(r *http.Request) (web.ApiResponse[infra.SandboxServices], *web.ApiError) {
	var resp web.ApiResponse[infra.SandboxServices]
	sbx, apiErr := sc.getSandboxOfUser(r.Context(), r.PathValue("sandboxID"), nil)
	if apiErr != nil {
		return resp, apiErr
	}
	result, err := sc.manager.DescribeSandboxServices(r.Context(), GetUserFromContext(r.Context()).ID.String(), sbx)
	if err != nil {
		return resp, sandboxServiceError(err)
	}
	resp.Body = result
	return resp, nil
}
func (sc *Controller) ExecSandboxService(r *http.Request) (web.ApiResponse[infra.ServiceResult], *web.ApiError) {
	return sc.runSandboxService(r, "exec")
}
func (sc *Controller) StopSandboxService(r *http.Request) (web.ApiResponse[infra.ServiceResult], *web.ApiError) {
	return sc.runSandboxService(r, "stop")
}
func (sc *Controller) runSandboxService(r *http.Request, kind string) (web.ApiResponse[infra.ServiceResult], *web.ApiError) {
	var resp web.ApiResponse[infra.ServiceResult]
	sbx, apiErr := sc.getSandboxOfUser(r.Context(), r.PathValue("sandboxID"), nil)
	if apiErr != nil {
		return resp, apiErr
	}
	request, apiErr := serviceRequest(r)
	if apiErr != nil {
		return resp, apiErr
	}
	if kind == "stop" && len(request.Args) > 0 {
		return resp, &web.ApiError{Code: 400, Message: "stop does not accept args"}
	}
	result, err := sc.manager.OperateSandboxService(r.Context(), GetUserFromContext(r.Context()).ID.String(), sbx, infra.SandboxServiceAction{Service: r.PathValue("service"), Kind: kind, PodUID: request.PodUID, Args: request.Args, TimeoutSeconds: request.TimeoutSeconds})
	if err != nil {
		return resp, sandboxServiceError(err)
	}
	resp.Body = result
	return resp, nil
}
func (sc *Controller) DownloadSandboxServiceFile(r *http.Request) (web.ApiResponse[web.RawBody], *web.ApiError) {
	var resp web.ApiResponse[web.RawBody]
	sbx, apiErr := sc.getSandboxOfUser(r.Context(), r.PathValue("sandboxID"), nil)
	if apiErr != nil {
		return resp, apiErr
	}
	uid := strings.Trim(r.Header.Get("If-Match"), "\"")
	result, err := sc.manager.OperateSandboxService(r.Context(), GetUserFromContext(r.Context()).ID.String(), sbx, infra.SandboxServiceAction{Service: r.PathValue("service"), Kind: "download", PodUID: uid, Source: r.URL.Query().Get("path"), TimeoutSeconds: 30})
	if err != nil {
		return resp, sandboxServiceError(err)
	}
	hash := sha256.Sum256(result.Stdout)
	resp.Body = web.RawBody(result.Stdout)
	resp.Headers = map[string]string{"Content-Type": "application/octet-stream", "Content-Length": strconv.Itoa(len(result.Stdout)), "Content-Disposition": mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(r.URL.Query().Get("path"))}), "X-Content-SHA256": hex.EncodeToString(hash[:]), "Cache-Control": "no-store"}
	return resp, nil
}
