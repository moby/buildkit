package artifact

import (
	"context"
	"encoding/json"
	"path"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	Name     = "artifact.v0"
	InputKey = "input"

	digestChunkSize = 4 << 20
)

func Build(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
	inputState, err := resolveInputState(ctx, c)
	if err != nil {
		return nil, err
	}

	opts := c.BuildOpts().Opts
	artifactType := opts[exptypes.ExporterArtifactTypeKey]
	configMediaType := opts[exptypes.ExporterArtifactConfigTypeKey]

	var layers []exptypes.ArtifactLayer
	if err := json.Unmarshal([]byte(opts[exptypes.ExporterArtifactLayersKey]), &layers); err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to parse layers metadata")
	}
	layers, err = normalizeAndValidateArtifactMetadata(configMediaType, layers)
	if err != nil {
		return nil, err
	}

	inputDef, err := inputState.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to marshal input state")
	}
	inputRes, err := c.Solve(ctx, gateway.SolveRequest{
		Definition: inputDef.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to solve input state")
	}
	inputRef, err := inputRes.SingleRef()
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to resolve input ref")
	}

	configContent := []byte("{}")
	configDigest := digest.FromBytes(configContent)
	configDesc := ocispecs.Descriptor{
		MediaType: configMediaType,
		Digest:    configDigest,
		Size:      int64(len(configContent)),
	}

	st := llb.Scratch().
		File(llb.Mkdir("/blobs/sha256", 0755, llb.WithParents(true))).
		File(llb.Mkfile("/blobs/sha256/"+configDigest.Encoded(), 0644, configContent))

	layerDescs := make([]ocispecs.Descriptor, 0, len(layers))
	for _, layer := range layers {
		stat, err := inputRef.StatFile(ctx, gateway.StatRequest{Path: layer.Path})
		if err != nil {
			return nil, errors.Wrapf(err, "artifact frontend: failed to stat layer file %s relative to input root", layer.Path)
		}
		if stat.IsDir() {
			return nil, errors.Errorf("artifact frontend: layer path %s must be a file", layer.Path)
		}

		dgst, err := digestFile(ctx, inputRef, layer.Path, stat.Size)
		if err != nil {
			return nil, err
		}
		desc := ocispecs.Descriptor{
			MediaType:   layer.MediaType,
			Digest:      dgst,
			Size:        stat.Size,
			Annotations: layer.Annotations,
		}
		layerDescs = append(layerDescs, desc)

		st = st.File(llb.Copy(inputState, layer.Path, "/blobs/sha256/"+dgst.Encoded(), &llb.CopyInfo{
			FollowSymlinks: true,
		}))
	}

	manifest := ocispecs.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispecs.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    layerDescs,
	}
	if artifactType != "" {
		manifest.ArtifactType = artifactType
	}

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to marshal manifest")
	}
	manifestDigest := digest.FromBytes(manifestJSON)

	index := ocispecs.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		Manifests: []ocispecs.Descriptor{{
			MediaType:    ocispecs.MediaTypeImageManifest,
			Digest:       manifestDigest,
			Size:         int64(len(manifestJSON)),
			ArtifactType: artifactType,
		}},
	}
	indexJSON, err := json.Marshal(index)
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to marshal index")
	}

	layoutJSON, err := json.Marshal(ocispecs.ImageLayout{Version: ocispecs.ImageLayoutVersion})
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to marshal oci-layout")
	}

	st = st.File(llb.Mkfile("/blobs/sha256/"+manifestDigest.Encoded(), 0644, manifestJSON))
	st = st.File(llb.Mkfile("/index.json", 0644, indexJSON))
	st = st.File(llb.Mkfile("/oci-layout", 0644, layoutJSON))

	def, err := st.Marshal(ctx, llb.WithCustomName("[artifact] assembling OCI layout"))
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to marshal OCI layout LLB")
	}

	res, err := c.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: failed to solve OCI layout assembly")
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, errors.Wrap(err, "artifact frontend: solve returned no ref")
	}

	out := gateway.NewResult()
	out.SetRef(ref)
	return out, nil
}

func resolveInputState(ctx context.Context, c gateway.Client) (llb.State, error) {
	inputs, err := c.Inputs(ctx)
	if err != nil {
		return llb.State{}, errors.Wrap(err, "artifact frontend: failed to load inputs")
	}
	if st, ok := inputs[InputKey]; ok {
		return st, nil
	}
	if len(inputs) == 1 {
		for _, st := range inputs {
			return st, nil
		}
	}
	return llb.State{}, errors.New("artifact frontend: missing input state")
}

func normalizeAndValidateArtifactMetadata(configMediaType string, layers []exptypes.ArtifactLayer) ([]exptypes.ArtifactLayer, error) {
	if configMediaType == "" {
		return nil, errors.New("artifact frontend: config descriptor mediaType is required")
	}
	normalized := make([]exptypes.ArtifactLayer, len(layers))
	for i, layer := range layers {
		cleanedPath, err := normalizeLayerPath(layer.Path)
		if err != nil {
			return nil, errors.Wrapf(err, "artifact frontend: invalid layer %d path", i)
		}
		if layer.MediaType == "" {
			return nil, errors.Errorf("artifact frontend: layer %d descriptor mediaType is required", i)
		}
		layer.Path = cleanedPath
		normalized[i] = layer
	}
	return normalized, nil
}

func normalizeLayerPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	cleaned := path.Clean(p)
	switch cleaned {
	case ".", "/":
		return "", errors.New("path must reference a file within the artifact input root")
	case "..":
		return "", errors.New("path must be relative to the artifact input root")
	}
	if cleaned[0] == '/' || len(cleaned) >= 3 && cleaned[:3] == "../" {
		return "", errors.New("path must be relative to the artifact input root")
	}
	return cleaned, nil
}

func digestFile(ctx context.Context, ref gateway.Reference, path string, size int64) (digest.Digest, error) {
	digester := digest.Canonical.Digester()
	hash := digester.Hash()

	for offset := int64(0); offset < size; {
		length := digestChunkSize
		if remaining := size - offset; remaining < int64(length) {
			length = int(remaining)
		}

		dt, err := ref.ReadFile(ctx, gateway.ReadRequest{
			Filename: path,
			Range: &gateway.FileRange{
				Offset: int(offset),
				Length: length,
			},
		})
		if err != nil {
			return "", errors.Wrapf(err, "artifact frontend: failed to read layer file %s", path)
		}
		if len(dt) == 0 && length > 0 {
			return "", errors.Errorf("artifact frontend: short read while hashing %s", path)
		}
		if _, err := hash.Write(dt); err != nil {
			return "", errors.Wrapf(err, "artifact frontend: failed to hash layer file %s", path)
		}
		offset += int64(len(dt))
	}

	return digester.Digest(), nil
}
