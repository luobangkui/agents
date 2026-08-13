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

package job

import (
	"regexp"
	"testing"
)

func TestInferOCIBaseImageFromDeliveryRef(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{
			in:     "dp-harbor-registry.cn-zhangjiakou.cr.aliyuncs.com/dptech/dp/native/prod-215/87/py313:delivery-e133a76b3565954da5b05ac644fbd669f541877346709ced9609b620af99bf39-v3",
			want:   "dp-harbor-registry.cn-zhangjiakou.cr.aliyuncs.com/dptech/dp/native/prod-215/87/py313@sha256:e133a76b3565954da5b05ac644fbd669f541877346709ced9609b620af99bf39",
			wantOK: true,
		},
		{in: "registry/repo:latest", wantOK: false},
		{in: "", wantOK: false},
	}
	for _, tt := range tests {
		got, ok := InferOCIBaseImageFromDeliveryRef(tt.in)
		if ok != tt.wantOK {
			t.Fatalf("ok=%v want %v for %q", ok, tt.wantOK, tt.in)
		}
		if got != tt.want {
			t.Fatalf("got %q want %q", got, tt.want)
		}
	}
}

func TestResolveBaseImage(t *testing.T) {
	explicit := "reg/repo@sha256:abc"
	if got := ResolveBaseImage(explicit, "reg/repo:delivery-"+regexp.MustCompile(`[a-f0-9]{64}`).FindString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")+"-v3"); got != explicit {
		t.Fatalf("explicit should win, got %q", got)
	}
	src := "reg/repo:delivery-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-v2"
	want := "reg/repo@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if got := ResolveBaseImage("", src); got != want {
		t.Fatalf("infer got %q want %q", got, want)
	}
}
