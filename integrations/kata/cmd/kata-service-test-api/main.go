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
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/openkruise/agents/katalab/pkg/infra/kataruntime"
	"github.com/openkruise/agents/katalab/pkg/manager"
	"github.com/openkruise/agents/katalab/pkg/servers/katabridge"
	"github.com/openkruise/agents/pkg/sandbox-manager/auditservice"
)

func run() error {
	tokenPath := flag.String("token-file", "/work/service-token", "private operator token")
	identityFile := flag.String("provider-identity-file", "", "trusted independent deployment incarnation")
	flag.Parse()
	raw, e := os.ReadFile(*tokenPath)
	if e != nil {
		return e
	}
	b, e := kataruntime.Open(kataruntime.Config{Root: "/work/api-state", Socket: "/work/containerd/state/containerd.sock"})
	if e != nil {
		return e
	}
	defer b.Close()
	h, e := katabridge.New(manager.New(b), auditservice.New(b), strings.TrimSpace(string(raw)))
	if e != nil {
		return e
	}
	if *identityFile != "" {
		identity, err := os.ReadFile(*identityFile)
		if err != nil {
			return err
		}
		h, e = katabridge.WithIdentity(h, identity, strings.TrimSpace(string(raw)))
		if e != nil {
			return e
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: "127.0.0.1:38125", Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		c, done := context.WithTimeout(context.Background(), 40*time.Second)
		defer done()
		if e := server.Shutdown(c); e != nil {
			fmt.Fprintln(os.Stderr, e)
		}
	}()
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
