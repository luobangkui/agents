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
package kataruntime

import (
	"encoding/json"
	"testing"

	"github.com/openkruise/agents/pkg/sandbox-manager/infra"
)

func TestBindingSurvivesDurableEncodingButRejectsSubstitution(t *testing.T) {
	a := infra.ServiceTarget{ExecutionID: "ta1-execution", OwnerID: "owner", InstanceID: "boot", Containers: map[string]string{"main": "cid", "observer": "obs"}, PolicyReceipt: json.RawMessage(`{"source":"trusted","pids_max":32}`)}
	raw, e := json.MarshalIndent(a, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	var b infra.ServiceTarget
	if e = json.Unmarshal(raw, &b); e != nil {
		t.Fatal(e)
	}
	if !sameTarget(a, b) {
		t.Fatal("durable formatting changed identity")
	}
	b.InstanceID = "replacement"
	if sameTarget(a, b) {
		t.Fatal("new guest accepted")
	}
	b.InstanceID = a.InstanceID
	b.PolicyReceipt = json.RawMessage(`{"source":"trusted","pids_max":33}`)
	if sameTarget(a, b) {
		t.Fatal("altered quota evidence accepted")
	}
	b.PolicyReceipt = json.RawMessage(`{"source":"trusted","pids_max":32,"extra":true}`)
	if sameTarget(a, b) {
		t.Fatal("altered policy accepted")
	}
}
