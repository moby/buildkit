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
	Img          ocispecs.Image
	Layers       []map[string]*TarItem
	LayersRaw    [][]byte
	descPlatform string
}

type ImageInfos []*ImageInfo

func (infos ImageInfos) Find(platform string) *ImageInfo {
	result := infos.Filter(platform)
	if len(result) == 0 {
		return nil
	}
	return result[0]
}

func (infos ImageInfos) Filter(platform string) ImageInfos {
	result := ImageInfos{}
	for _, info := range infos {
		if info.descPlatform == platform {
			result = append(result, info)
		}
	}
	return result
}

func ReadIndex(ctx context.Context, p content.Provider, desc ocispecs.Descriptor) (ImageInfos, error) {
	infos := ImageInfos{}

	dt, err := content.ReadBlob(ctx, p, desc)
	if err != nil {
		return nil, err
	}
	var idx ocispecs.Index
	if err := json.Unmarshal(dt, &idx); err != nil {
		return nil, err
	}

	for _, m := range idx.Manifests {
		img, err := ReadImage(ctx, p, m)
		if err != nil {
			return nil, err
		}
		img.descPlatform = platforms.Format(*m.Platform)
		infos = append(infos, img)
	}
	return infos, nil
}

func ReadImage(ctx context.Context, p content.Provider, desc ocispecs.Descriptor) (*ImageInfo, error) {
	ii := &ImageInfo{Desc: desc}

	dt, err := content.ReadBlob(ctx, p, desc)
	if err != nil {
		return nil, err
	}
	var mfst ocispecs.Manifest
	if err := json.Unmarshal(dt, &mfst); err != nil {
		return nil, err
	}

	dt, err = content.ReadBlob(ctx, p, mfst.Config)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(dt, &ii.Img); err != nil {
		return nil, err
	}

	ii.Layers = make([]map[string]*TarItem, len(mfst.Layers))
	ii.LayersRaw = make([][]byte, len(mfst.Layers))
	for i, l := range mfst.Layers {
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
