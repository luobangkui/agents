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

// Package oss provides the Alibaba Cloud OSS CSI storage runtime adapter.
package oss

import (
	"context"
	"fmt"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"github.com/openkruise/agents/pkg/agent-runtime/storage-cli/storage"
)

const driverName = "ossplugin.csi.alibabacloud.com"

type provider struct{}

var runNodeUnpublishVolumeFn = storage.RunNodeUnpublishVolume

func init() {
	storage.Register(&provider{})
}

func (p *provider) Driver() string {
	return driverName
}

func (p *provider) SubDir() string {
	return "oss"
}

func (p *provider) Validate(req csi.NodePublishVolumeRequest) error {
	if strings.TrimSpace(req.VolumeId) == "" {
		return fmt.Errorf("OSS CSI volume ID is required")
	}
	if strings.TrimSpace(req.TargetPath) == "" {
		return fmt.Errorf("OSS CSI target path is required")
	}
	if req.VolumeCapability == nil {
		return fmt.Errorf("OSS CSI volume capability is required")
	}
	if strings.TrimSpace(req.VolumeContext["bucket"]) == "" {
		return fmt.Errorf("OSS CSI volume context bucket is required")
	}
	if strings.TrimSpace(req.VolumeContext["url"]) == "" {
		return fmt.Errorf("OSS CSI volume context url is required")
	}
	return nil
}

func (p *provider) Mount(ctx context.Context, req csi.NodePublishVolumeRequest, debug bool) error {
	return storage.RunNodePublishVolume(ctx, driverName, req, debug)
}

func (p *provider) Unmount(ctx context.Context, req csi.NodePublishVolumeRequest) error {
	return runNodeUnpublishVolumeFn(ctx, driverName, req)
}

var _ storage.Provider = (*provider)(nil)
