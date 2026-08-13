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

// Command commit-rebase rewrites a locally committed image so its parent layers
// come from an OCI base image, while keeping the committed upper layer.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/namespaces"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func main() {
	sock := flag.String("sock", "/run/containerd/containerd.sock", "containerd socket")
	namespace := flag.String("namespace", "k8s.io", "containerd namespace")
	image := flag.String("image", "", "committed image reference to rewrite")
	base := flag.String("base", "", "OCI base image reference (repo@sha256:...)")
	checkOnly := flag.Bool("check-nydus", false, "only report whether image has nydus layers (exit 0=yes, 1=no, 2=error)")
	flag.Parse()

	if *image == "" {
		fail("missing --image")
	}
	ctx := namespaces.WithNamespace(context.Background(), *namespace)
	client, err := containerd.New(*sock, containerd.WithDefaultNamespace(*namespace))
	if err != nil {
		fail("connect containerd: %v", err)
	}
	defer client.Close()

	if *checkOnly {
		has, err := hasNydusLayers(ctx, client, *image)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check-nydus: %v\n", err)
			os.Exit(2)
		}
		if has {
			fmt.Println("nydus=true")
			os.Exit(0)
		}
		fmt.Println("nydus=false")
		os.Exit(1)
	}

	if *base == "" {
		fail("missing --base")
	}
	if err := rebase(ctx, client, *image, *base); err != nil {
		fail("%v", err)
	}
	fmt.Printf("rebased %s onto %s\n", *image, *base)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func hasNydusLayers(ctx context.Context, client *containerd.Client, ref string) (bool, error) {
	meta, err := client.ImageService().Get(ctx, ref)
	if err != nil {
		return false, err
	}
	manifest, err := readManifest(ctx, client.ContentStore(), meta.Target)
	if err != nil {
		return false, err
	}
	return manifestHasNydus(manifest.Layers), nil
}

func rebase(ctx context.Context, client *containerd.Client, committedRef, baseRef string) error {
	cs := client.ContentStore()
	is := client.ImageService()

	committedMeta, err := is.Get(ctx, committedRef)
	if err != nil {
		return fmt.Errorf("get committed image %q: %w", committedRef, err)
	}
	baseMeta, err := is.Get(ctx, baseRef)
	if err != nil {
		return fmt.Errorf("get base image %q: %w", baseRef, err)
	}

	committedManifest, err := readManifest(ctx, cs, committedMeta.Target)
	if err != nil {
		return fmt.Errorf("read committed manifest: %w", err)
	}
	baseManifest, err := readManifest(ctx, cs, baseMeta.Target)
	if err != nil {
		return fmt.Errorf("read base manifest: %w", err)
	}
	if len(committedManifest.Layers) == 0 {
		return fmt.Errorf("committed image has no layers")
	}
	if len(baseManifest.Layers) == 0 {
		return fmt.Errorf("base image has no layers")
	}
	if manifestHasNydus(baseManifest.Layers) {
		return fmt.Errorf("base image %q has nydus layers", baseRef)
	}

	upperLayer := committedManifest.Layers[len(committedManifest.Layers)-1]
	if strings.Contains(strings.ToLower(upperLayer.MediaType), "nydus") {
		return fmt.Errorf("upper layer media type is nydus: %s", upperLayer.MediaType)
	}

	committedCfg, err := readConfig(ctx, cs, committedManifest.Config)
	if err != nil {
		return fmt.Errorf("read committed config: %w", err)
	}
	baseCfg, err := readConfig(ctx, cs, baseManifest.Config)
	if err != nil {
		return fmt.Errorf("read base config: %w", err)
	}
	if len(committedCfg.RootFS.DiffIDs) == 0 {
		return fmt.Errorf("committed config has no DiffIDs")
	}

	upperDiffID := committedCfg.RootFS.DiffIDs[len(committedCfg.RootFS.DiffIDs)-1]
	newCfg := baseCfg
	created := time.Now().UTC()
	newCfg.Created = &created
	newCfg.RootFS = ocispec.RootFS{
		Type:    "layers",
		DiffIDs: append(append([]digest.Digest{}, baseCfg.RootFS.DiffIDs...), upperDiffID),
	}
	if len(committedCfg.History) > 0 {
		newCfg.History = append(append([]ocispec.History{}, baseCfg.History...), committedCfg.History[len(committedCfg.History)-1])
	} else {
		newCfg.History = append(append([]ocispec.History{}, baseCfg.History...), ocispec.History{
			Created: &created,
			Comment: "rebase upper onto OCI base",
		})
	}

	newConfigJSON, err := json.Marshal(newCfg)
	if err != nil {
		return err
	}
	configDesc := ocispec.Descriptor{
		MediaType: images.MediaTypeDockerSchema2Config,
		Digest:    digest.FromBytes(newConfigJSON),
		Size:      int64(len(newConfigJSON)),
	}
	if err := content.WriteBlob(ctx, cs, configDesc.Digest.String(), bytes.NewReader(newConfigJSON), configDesc,
		content.WithLabels(map[string]string{
			"containerd.io/gc.ref.snapshot.overlayfs": identity.ChainID(newCfg.RootFS.DiffIDs).String(),
		}),
	); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	newLayers := append(append([]ocispec.Descriptor{}, baseManifest.Layers...), upperLayer)
	newManifest := struct {
		MediaType string `json:"mediaType,omitempty"`
		ocispec.Manifest
	}{
		MediaType: images.MediaTypeDockerSchema2Manifest,
		Manifest: ocispec.Manifest{
			Versioned: specs.Versioned{SchemaVersion: 2},
			Config:    configDesc,
			Layers:    newLayers,
		},
	}
	newManifestJSON, err := json.MarshalIndent(newManifest, "", "    ")
	if err != nil {
		return err
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: images.MediaTypeDockerSchema2Manifest,
		Digest:    digest.FromBytes(newManifestJSON),
		Size:      int64(len(newManifestJSON)),
	}
	labels := map[string]string{"containerd.io/gc.ref.content.0": configDesc.Digest.String()}
	for i, l := range newLayers {
		labels[fmt.Sprintf("containerd.io/gc.ref.content.%d", i+1)] = l.Digest.String()
	}
	if err := content.WriteBlob(ctx, cs, manifestDesc.Digest.String(), bytes.NewReader(newManifestJSON), manifestDesc, content.WithLabels(labels)); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	committedMeta.Target = manifestDesc
	committedMeta.UpdatedAt = time.Now()
	if _, err := is.Update(ctx, committedMeta, "target"); err != nil {
		return fmt.Errorf("update image: %w", err)
	}
	return nil
}

func manifestHasNydus(layers []ocispec.Descriptor) bool {
	for _, l := range layers {
		if strings.Contains(strings.ToLower(l.MediaType), "nydus") {
			return true
		}
	}
	return false
}

func readManifest(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (ocispec.Manifest, error) {
	var m ocispec.Manifest
	b, err := content.ReadBlob(ctx, cs, desc)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func readConfig(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (ocispec.Image, error) {
	var cfg ocispec.Image
	b, err := content.ReadBlob(ctx, cs, desc)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(b, &cfg)
}
