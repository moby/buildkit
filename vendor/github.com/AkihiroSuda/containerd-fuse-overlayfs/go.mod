module github.com/AkihiroSuda/containerd-fuse-overlayfs

go 1.13

require (
	github.com/Microsoft/hcsshim v0.8.9 // indirect
	github.com/containerd/containerd v1.4.0-0
	github.com/containerd/continuity v0.0.0-20200228182428-0f16d7a0959c
	github.com/containerd/ttrpc v1.0.1 // indirect
	github.com/containerd/typeurl v1.0.1 // indirect
	github.com/coreos/go-systemd/v22 v22.0.0
	github.com/docker/go-events v0.0.0-20190806004212-e31b211e4f1c // indirect
	github.com/google/go-cmp v0.4.0 // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/opencontainers/runc v1.0.0-rc10 // indirect
	github.com/pkg/errors v0.9.1
	go.etcd.io/bbolt v1.3.3 // indirect
	golang.org/x/sync v0.0.0-20190911185100-cd5d95a43a6e // indirect
	golang.org/x/sys v0.0.0-20200302150141-5c8b2ff67527 // indirect
	google.golang.org/grpc v1.27.1
	gotest.tools/v3 v3.0.2 // indirect
)

replace github.com/containerd/containerd => github.com/containerd/containerd v1.3.1-0.20200511142502-04985039cede
