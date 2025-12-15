package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"

	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/gateway/grpcclient"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/bklog"
	_ "github.com/moby/buildkit/util/grpcutil/encoding/proto"
	"github.com/moby/buildkit/util/staticfs"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	fstypes "github.com/tonistiigi/fsutil/types"
)

func main() {
	if err := grpcclient.ExportFromEnvironment(appcontext.Context(), export); err != nil {
		bklog.L.Errorf("fatal error: %+v", err)
		panic(err)
	}
}

type report struct {
	Opts      map[string]string `json:"opts"`
	Target    string            `json:"target"`
	Platforms []string          `json:"platforms"`

	Refs map[string]*reportRef `json:"refs"`
}

type reportRef struct {
	Config json.RawMessage `json:"config"`

	AllFiles []string `json:"all_files"`

	Layers     []ocispecs.Descriptor      `json:"layers"`
	LayerFiles map[digest.Digest][]string `json:"layer_files"`

	Attestations []intoto.Statement `json:"attestations"`
}

func export(ctx context.Context, c client.Client, handle client.ExportHandle, res *client.Result) (err error) {
	opts := c.BuildOpts().Opts
	if opts == nil {
		opts = map[string]string{}
	}

	store := handle.ContentStore()

	report := &report{
		Opts:   opts,
		Target: string(handle.Target),
		Refs:   map[string]*reportRef{},
	}

	ps, err := exptypes.ParsePlatforms(res.Metadata)
	if err != nil {
		return err
	}
	for _, p := range ps.Platforms {
		report.Platforms = append(report.Platforms, p.ID)
		ref, ok := res.FindRef(p.ID)
		if !ok {
			return errors.Errorf("no ref for platform %s", p.ID)
		}

		reportRef := &reportRef{}
		report.Refs[p.ID] = reportRef

		reportRef.Config = exptypes.ParseKey(res.Metadata, exptypes.ExporterImageConfigKey, &p)

		err := walkDir(ctx, ref, "/", func(path string, info *fstypes.Stat) error {
			reportRef.AllFiles = append(reportRef.AllFiles, path)
			return nil
		})
		if err != nil {
			return errors.Wrapf(err, "failed to walk ref for platform %s", p.ID)
		}

		descs, err := ref.GetRemote(ctx, config.RefConfig{})
		if err != nil {
			return errors.Wrapf(err, "failed to get remote descs for platform %s", p.ID)
		}

		reportRef.LayerFiles = map[digest.Digest][]string{}
		for _, desc := range descs {
			reportRef.Layers = append(reportRef.Layers, desc)

			err := func() (rerr error) {
				r, err := store.ReaderAt(ctx, desc)
				if err != nil {
					return errors.Wrap(err, "failed to get reader for exported content")
				}
				defer func() {
					err := r.Close()
					if rerr == nil {
						rerr = err
					}
				}()

				sr := io.NewSectionReader(r, 0, r.Size())
				rr, err := gzip.NewReader(sr)
				if err != nil {
					return errors.Wrap(err, "failed to create gzip reader")
				}
				tr := tar.NewReader(rr)

				for {
					hdr, err := tr.Next()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						return errors.Wrap(err, "failed to read tar header")
					}
					reportRef.LayerFiles[desc.Digest] = append(reportRef.LayerFiles[desc.Digest], hdr.Name)
				}

				return nil
			}()
			if err != nil {
				return err
			}
		}

		var defaultSubjects []intoto.Subject
		defaultSubjects = append(defaultSubjects, intoto.Subject{
			Name:   "report.json",
			Digest: result.ToDigestMap(digest.FromString("report.json")),
		})
		atts, ok := res.Attestations[p.ID]
		if ok {
			stmts, err := MakeInTotoStatements(ctx, atts, defaultSubjects)
			if err != nil {
				return errors.Wrapf(err, "failed to make in-toto statements for platform %s", p.ID)
			}

			reportRef.Attestations = stmts
		}
	}

	if descs, ok := opts["fetch-descs"]; ok {
		var fetchDescs []ocispecs.Descriptor
		if err := json.Unmarshal([]byte(descs), &fetchDescs); err != nil {
			return errors.Wrap(err, "failed to unmarshal fetch-descs")
		}

		store := handle.ContentStore()
		for _, desc := range fetchDescs {
			r, err := store.ReaderAt(ctx, desc)
			if err != nil {
				return errors.Wrapf(err, "failed to get reader for fetch-desc %s", desc.Digest)
			}
			defer r.Close()
			sr := io.NewSectionReader(r, 0, r.Size())

			_, err = io.Copy(io.Discard, sr)
			if err != nil {
				return errors.Wrapf(err, "failed to read fetch-desc %s", desc.Digest)
			}
		}
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal report")
	}
	out = append(out, '\n')
	fmt.Fprint(os.Stderr, string(out))

	switch handle.Target {
	case exptypes.ExporterTargetNone:
	case exptypes.ExporterTargetFile:
		w, err := handle.SendFile(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(w, string(out))
		if err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	case exptypes.ExporterTargetDirectory:
		fs := staticfs.NewFS()
		fs.Add("report.json", &fstypes.Stat{Mode: 0644}, out)
		return handle.SendFS(ctx, fs)
	}

	return nil
}

func walkDir(ctx context.Context, ref client.Reference, root string, fn func(path string, info *fstypes.Stat) error) error {
	entries, err := ref.ReadDir(ctx, client.ReadDirRequest{Path: root})
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath := path.Join(root, entry.Path)
		if entry.IsDir() {
			entryPath += "/"
		}
		if err := fn(entryPath, entry); err != nil {
			return err
		}
		if entry.IsDir() {
			if err := walkDir(ctx, ref, entryPath, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
