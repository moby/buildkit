module github.com/moby/buildkit

go 1.13

require (
	github.com/AkihiroSuda/containerd-fuse-overlayfs v0.10.0
	github.com/BurntSushi/toml v0.3.1
	github.com/Microsoft/go-winio v0.4.15-0.20190919025122-fc70bd9a86b5
	github.com/Microsoft/hcsshim v0.8.9
	github.com/codahale/hdrhistogram v0.0.0-20160425231609-f8ad88b59a58 // indirect
	github.com/containerd/console v1.0.1
	github.com/containerd/containerd v1.4.1-0.20200903181227-d4e78200d6da
	github.com/containerd/continuity v0.0.0-20200710164510-efbc4488d8fe
	github.com/containerd/go-cni v1.0.1
	github.com/containerd/go-runc v0.0.0-20201020171139-16b287bc67d0
	github.com/containerd/stargz-snapshotter v0.0.0-20200903042824-2ee75e91f8f9
	github.com/containerd/typeurl v1.0.1
	github.com/coreos/go-systemd/v22 v22.1.0
	github.com/docker/cli v0.0.0-20200227165822-2298e6a3fe24
	github.com/docker/distribution v2.7.1+incompatible
	github.com/docker/docker v17.12.0-ce-rc1.0.20200730172259-9f28837c1d93+incompatible
	github.com/docker/go-connections v0.4.0
	github.com/docker/libnetwork v0.8.0-dev.2.0.20200226230617-d8334ccdb9be
	github.com/gofrs/flock v0.7.3
	github.com/gogo/googleapis v1.3.2
	github.com/gogo/protobuf v1.3.1
	// protobuf: the actual version is replaced in replace()
	github.com/golang/protobuf v1.4.2
	github.com/google/go-cmp v0.4.0
	github.com/google/shlex v0.0.0-20150127133951-6f45313302b9
	github.com/gorilla/mux v1.7.4 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware v1.2.0
	github.com/grpc-ecosystem/grpc-opentracing v0.0.0-20180507213350-8e809c8a8645
	github.com/hashicorp/go-immutable-radix v1.0.0
	github.com/hashicorp/golang-lru v0.5.3
	github.com/hashicorp/uuid v0.0.0-20160311170451-ebb0a03e909c // indirect
	github.com/ishidawataru/sctp v0.0.0-20191218070446-00ab2ac2db07 // indirect
	github.com/jaguilar/vt100 v0.0.0-20150826170717-2703a27b14ea
	github.com/mitchellh/hashstructure v1.0.0
	github.com/morikuni/aec v1.0.0
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.0.1
	github.com/opencontainers/runc v1.0.0-rc92
	github.com/opencontainers/runtime-spec v1.0.3-0.20200728170252-4d89ac9fbff6
	github.com/opentracing-contrib/go-stdlib v1.0.0
	github.com/opentracing/opentracing-go v1.2.0
	github.com/pkg/errors v0.9.1
	github.com/pkg/profile v1.5.0
	github.com/serialx/hashring v0.0.0-20190422032157-8b2912629002
	github.com/sirupsen/logrus v1.7.0
	github.com/stretchr/testify v1.5.1
	github.com/tonistiigi/fsutil v0.0.0-20200724193237-c3ed55f3b481
	github.com/tonistiigi/units v0.0.0-20180711220420-6950e57a87ea
	github.com/uber/jaeger-client-go v2.25.0+incompatible
	github.com/uber/jaeger-lib v2.2.0+incompatible // indirect
	github.com/urfave/cli v1.22.2
	go.etcd.io/bbolt v1.3.5
	golang.org/x/crypto v0.0.0-20200622213623-75b288015ac9
	golang.org/x/net v0.0.0-20200707034311-ab3426394381
	golang.org/x/sync v0.0.0-20190911185100-cd5d95a43a6e
	golang.org/x/sys v0.0.0-20200916030750-2334cc1a136f
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	// genproto: the actual version is replaced in replace()
	google.golang.org/genproto v0.0.0-20200526211855-cb27e3aa2013
	google.golang.org/grpc v1.28.1
)

replace (
	github.com/docker/docker => github.com/docker/docker v17.12.0-ce-rc1.0.20200310163718-4634ce647cf2+incompatible
	// protobuf: corresponds to containerd
	github.com/golang/protobuf => github.com/golang/protobuf v1.3.5
	github.com/hashicorp/go-immutable-radix => github.com/tonistiigi/go-immutable-radix v0.0.0-20170803185627-826af9ccf0fe
	github.com/jaguilar/vt100 => github.com/tonistiigi/vt100 v0.0.0-20190402012908-ad4c4a574305
	// genproto: corresponds to containerd
	google.golang.org/genproto => google.golang.org/genproto v0.0.0-20200224152610-e50cd9704f63
)
