package testutil

import (
	"context"
	"encoding/json"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type ImageInfo struct {
	Desc         ocispecs.Descriptor
	Manifest     ocispecs.Manifest
	Img          ocispecs.Image
	Layers       []map[string]*TarItem
	LayersRaw    [][]byte
	descPlatform string
}

type ImagesInfo struct {
	Desc   ocispecs.Descriptor
	Index  ocispecs.Index
	Images []*ImageInfo
}

func (idx ImagesInfo) Find(platform string) *ImageInfo {
	result := idx.Filter(platform)
	if len(result.Images) == 0 {
		return nil
	}
	return result.Images[0]
}

func (idx ImagesInfo) Filter(platform string) *ImagesInfo {
	result := &ImagesInfo{Desc: idx.Desc}
	for _, info := range idx.Images {
		if info.descPlatform == platform {
			result.Images = append(result.Images, info)
		}
	}
	return result
}

func ReadImages(ctx context.Context, p content.Provider, desc ocispecs.Descriptor) (*ImagesInfo, error) {
	idx := &ImagesInfo{Desc: desc}

	dt, err := content.ReadBlob(ctx, p, desc)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(dt, &idx.Index); err != nil {
		return nil, err
	}

	for _, m := range idx.Index.Manifests {
		img, err := ReadImage(ctx, p, m)
		if err != nil {
			return nil, err
		}
		img.descPlatform = platforms.Format(*m.Platform)
		idx.Images = append(idx.Images, img)
	}
	return idx, nil
}

func ReadImage(ctx context.Context, p content.Provider, desc ocispecs.Descriptor) (*ImageInfo, error) {
	ii := &ImageInfo{Desc: desc}

	dt, err := content.ReadBlob(ctx, p, desc)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(dt, &ii.Manifest); err != nil {
		return nil, err
	}

	dt, err = content.ReadBlob(ctx, p, ii.Manifest.Config)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(dt, &ii.Img); err != nil {
		return nil, err
	}

	ii.Layers = make([]map[string]*TarItem, len(ii.Manifest.Layers))
	ii.LayersRaw = make([][]byte, len(ii.Manifest.Layers))
	for i, l := range ii.Manifest.Layers {
		dt, err := content.ReadBlob(ctx, p, l)
		if err != nil {
			return nil, err
		}
		ii.LayersRaw[i] = dt
		if images.IsLayerType(l.MediaType) {
			m, err := ReadTarToMap(dt, true)
			if err != nil {
				return nil, err
			}
			ii.Layers[i] = m
		}
	}
	return ii, nil
}
