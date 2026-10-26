package blobfetch

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/images"
	srctypes "github.com/moby/buildkit/source/types"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestFetchBlobAcceptsManifestsAndBlobs(t *testing.T) {
	for _, mediaType := range []string{
		ocispecs.MediaTypeImageManifest,
		ocispecs.MediaTypeImageIndex,
		images.MediaTypeDockerSchema2Manifest,
		images.MediaTypeDockerSchema2ManifestList,
		"application/octet-stream",
	} {
		t.Run(mediaType, func(t *testing.T) {
			body := fmt.Appendf(nil, `{"mediaType":%q}`, mediaType)
			dgst := digest.FromBytes(body)
			endpoint := "manifests"
			if mediaType == "application/octet-stream" {
				endpoint = "blobs"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/repo/"+endpoint+"/"+dgst.String() {
					http.NotFound(w, r)
					return
				}
				// Distribution's OCI manifest endpoint rejects a bare
				// wildcard even when the client addresses a digest.
				if endpoint == "manifests" && !strings.Contains(r.Header.Get("Accept"), mediaType) {
					http.Error(w, "manifest media type not accepted", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				w.Header().Set("Content-Type", mediaType)
				if r.Method == http.MethodGet {
					_, _ = w.Write(body)
				}
			}))
			defer server.Close()

			rc, got, err := FetchBlob(t.Context(), nil, FetchOpt{
				Scheme: srctypes.DockerImageBlobScheme,
				Ref:    strings.TrimPrefix(server.URL, "http://") + "/repo@" + dgst.String(),
				Digest: dgst,
			})
			require.NoError(t, err)
			defer rc.Close()
			actual, err := io.ReadAll(rc)
			require.NoError(t, err)
			require.Equal(t, dgst, got)
			require.Equal(t, body, actual)
		})
	}
}
