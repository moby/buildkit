package uploadprovider

import (
	"io"
	"path"
	"sync"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session/upload"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func New() *Uploader {
	return &Uploader{m: map[string]io.ReadCloser{}}
}

type Uploader struct {
	mu sync.Mutex
	m  map[string]io.ReadCloser
}

func (hp *Uploader) Add(r io.ReadCloser) string {
	id := identity.NewID()
	hp.m[id] = r
	return "http://buildkit-session/" + id
}

func (hp *Uploader) Register(server *grpc.Server) {
	upload.RegisterUploadServer(server, hp)
}

func (hp *Uploader) Pull(stream upload.Upload_PullServer) error {
	opts, _ := metadata.FromIncomingContext(stream.Context()) // if no metadata continue with empty object
	var p string
	urls, ok := opts["urlpath"]
	if ok && len(urls) > 0 {
		p = urls[0]
	}

	p = path.Base(p)

	hp.mu.Lock()
	r, ok := hp.m[p]
	if !ok {
		hp.mu.Unlock()
		return errors.Errorf("no http response from session for %s", p)
	}
	delete(hp.m, p)
	hp.mu.Unlock()

	_, err := io.Copy(&writer{stream}, r)

	if err1 := r.Close(); err == nil {
		err = err1
	}

	return err
}

type writer struct {
	grpc.ServerStream
}

func (w *writer) Write(dt []byte) (n int, err error) {
	const maxChunkSize = 3 * 1024 * 1024
	for len(dt) > 0 {
		data := dt
		if len(data) > maxChunkSize {
			data = data[:maxChunkSize]
		}

		msg := &upload.BytesMessage{Data: data}
		if err := w.SendMsg(msg); err != nil {
			return n, err
		}
		n += len(data)
		dt = dt[len(data):]
	}
	return n, nil
}
