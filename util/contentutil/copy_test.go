package contentutil

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestCopy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	b0 := NewBuffer()
	b1 := NewBuffer()

	err := content.WriteBlob(ctx, b0, "foo", bytes.NewBuffer([]byte("foobar")), ocispecs.Descriptor{Size: -1})
	require.NoError(t, err)

	err = Copy(ctx, b1, b0, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar")), Size: -1}, "", nil)
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, b1, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar"))})
	require.NoError(t, err)
	require.Equal(t, "foobar", string(dt))
}

func TestCopyRegistryRetryAfterEOF(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeoutCause(t.Context(), 10*time.Second, context.DeadlineExceeded)
	defer cancel()

	payload := []byte("registry cache layer data")
	desc := ocispecs.Descriptor{
		MediaType: ocispecs.MediaTypeImageLayer,
		Digest:    digest.FromBytes(payload),
		Size:      int64(len(payload)),
	}
	var attempts atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Location", "/v2/cache/blobs/uploads/test")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !bytes.Equal(body, payload) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusRequestTimeout)
			return
		}
		w.Header().Set("Docker-Content-Digest", desc.Digest.String())
		w.WriteHeader(http.StatusCreated)
	}))
	defer registry.Close()

	ref := strings.TrimPrefix(registry.URL, "http://") + "/cache:latest"
	resolver := docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})
	pusher, err := resolver.Pusher(ctx, ref)
	require.NoError(t, err)
	provider := NewBuffer()
	require.NoError(t, content.WriteBlob(ctx, provider, "source", bytes.NewReader(payload), desc))
	require.NoError(t, Copy(ctx, FromPusher(pusher), provider, desc, ref, nil))
	require.EqualValues(t, 2, attempts.Load())
}
