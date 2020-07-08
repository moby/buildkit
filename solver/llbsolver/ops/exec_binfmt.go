package ops

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/binfmt_misc"
	"github.com/moby/buildkit/util/progress"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	copy "github.com/tonistiigi/fsutil/copy"
)

const qemuMountName = "/dev/.buildkit_qemu_emulator"
const emulatorImage = "docker.io/tonistiigi/binfmt:buildkit@sha256:15b3561ce399a84f27ddd83b82e10c26c31ebab0b19a49aee584498963cba7af"

var qemuArchMap = map[string]string{
	"arm64":   "aarch64",
	"amd64":   "x86_64",
	"riscv64": "riscv64",
	"arm":     "arm",
	"s390x":   "s390x",
	"ppc64le": "ppc64le",
}

type emulator struct {
	mount   snapshot.Mountable
	release func(context.Context) error
}

func (e *emulator) Mount(ctx context.Context, readonly bool) (snapshot.Mountable, error) {
	return e.mount, nil
}

func (e *emulator) Release(ctx context.Context) error {
	if e.release != nil {
		return e.release(ctx)
	}
	return nil
}

type refEmulatorMount struct {
	ref  cache.ImmutableRef
	name string
}

func (m *refEmulatorMount) Mount() ([]mount.Mount, func() error, error) {
	mountable, err := m.ref.Mount(context.TODO(), true)
	if err != nil {
		return nil, nil, err
	}
	mounter := snapshot.LocalMounter(mountable)
	release := func() error {
		return mounter.Unmount()
	}
	target, err := mounter.Mount()
	if err != nil {
		release()
		return nil, nil, err
	}

	return []mount.Mount{{
		Type:    "bind",
		Source:  filepath.Join(target, "buildkit-qemu-"+m.name),
		Options: []string{"ro", "bind"},
	}}, release, nil
}

func (m *refEmulatorMount) IdentityMapping() *idtools.IdentityMapping {
	return m.ref.IdentityMapping()
}

type staticEmulatorMount struct {
	path  string
	idmap *idtools.IdentityMapping
}

func (m *staticEmulatorMount) Mount() ([]mount.Mount, func() error, error) {
	tmpdir, err := ioutil.TempDir("", "buildkit-qemu-emulator")
	if err != nil {
		return nil, nil, err
	}
	var ret bool
	defer func() {
		if !ret {
			os.RemoveAll(tmpdir)
		}
	}()

	var uid, gid int
	if m.idmap != nil {
		root := m.idmap.RootPair()
		uid = root.UID
		gid = root.GID
	}
	if err := copy.Copy(context.TODO(), filepath.Dir(m.path), filepath.Base(m.path), tmpdir, qemuMountName, func(ci *copy.CopyInfo) {
		m := 0555
		ci.Mode = &m
	}, copy.WithChown(uid, gid)); err != nil {
		return nil, nil, err
	}

	ret = true
	return []mount.Mount{{
			Type:    "bind",
			Source:  filepath.Join(tmpdir, qemuMountName),
			Options: []string{"ro", "bind"},
		}}, func() error {
			return os.RemoveAll(tmpdir)
		}, nil

}
func (m *staticEmulatorMount) IdentityMapping() *idtools.IdentityMapping {
	return m.idmap
}

func getEmulator(ctx context.Context, src *source.Manager, cs content.Store, sm *session.Manager, p *pb.Platform, idmap *idtools.IdentityMapping) (*emulator, error) {
	all := binfmt_misc.SupportedPlatforms(false)
	m := make(map[string]struct{}, len(all))

	for _, p := range all {
		m[p] = struct{}{}
	}

	pp := platforms.Normalize(specs.Platform{
		Architecture: p.Architecture,
		OS:           p.OS,
		Variant:      p.Variant,
	})

	if _, ok := m[platforms.Format(pp)]; ok {
		return nil, nil
	}

	a, ok := qemuArchMap[pp.Architecture]
	if !ok {
		a = pp.Architecture
	}

	fn, err := exec.LookPath("buildkit-qemu-" + a)
	if err == nil {
		return &emulator{mount: &staticEmulatorMount{path: fn, idmap: idmap}}, nil
	}

	return pullEmulator(ctx, src, cs, sm, pp, a)
}

func pullEmulator(ctx context.Context, src *source.Manager, cs content.Store, sm *session.Manager, p specs.Platform, name string) (_ *emulator, err error) {
	id, err := source.NewImageIdentifier(emulatorImage)
	if err != nil {
		return nil, err
	}
	s := platforms.DefaultSpec()
	id.Platform = &s
	id.RecordType = client.UsageRecordTypeInternal

	spec, err := reference.Parse(emulatorImage)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var exists bool
	if dgst := spec.Digest(); dgst != "" {
		if _, err := cs.Info(ctx, dgst); err == nil {
			exists = true
		}
	}
	if !exists {
		defer oneOffProgress(ctx, fmt.Sprintf("pulling emulator for %s", platforms.Format(p)))(err)
	}

	ctx = progress.WithProgress(ctx, &discard{})

	inst, err := src.Resolve(ctx, id, sm)
	if err != nil {
		return nil, err
	}

	ref, err := inst.Snapshot(ctx)
	if err != nil {
		return nil, err
	}

	return &emulator{mount: &refEmulatorMount{ref: ref, name: name}, release: ref.Release}, nil
}

func oneOffProgress(ctx context.Context, id string) func(err error) error {
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func(err error) error {
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
		return err
	}
}

type discard struct {
}

func (d *discard) Write(id string, value interface{}) error {
	return nil
}
func (d *discard) Close() error {
	return nil
}
