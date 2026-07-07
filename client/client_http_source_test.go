package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBuildHTTPSource(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	modTime := time.Now().Add(-24 * time.Hour) // avoid falso positive with current time

	resp := &httpserver.Response{
		Etag:         identity.NewID(),
		Content:      []byte("content1"),
		LastModified: &modTime,
	}

	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	// invalid URL first
	st := llb.HTTP(server.URL + "/bar")

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid response status 404")

	// first correct request
	st = llb.HTTP(server.URL + "/foo")

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	require.Equal(t, 1, server.Stats("/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/foo").CachedRequests)

	tmpdir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: tmpdir,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, 2, server.Stats("/foo").AllRequests)
	require.Equal(t, 1, server.Stats("/foo").CachedRequests)

	dt, err := os.ReadFile(filepath.Join(tmpdir, "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("content1"), dt)

	allReqs := server.Stats("/foo").Requests
	require.Equal(t, 2, len(allReqs))
	require.Equal(t, http.MethodGet, allReqs[0].Method)
	require.Equal(t, "gzip", allReqs[0].Header.Get("Accept-Encoding"))
	require.Equal(t, http.MethodHead, allReqs[1].Method)
	require.Equal(t, "gzip", allReqs[1].Header.Get("Accept-Encoding"))

	require.NoError(t, os.RemoveAll(filepath.Join(tmpdir, "foo")))

	// update the content at the url to be gzipped now, the final output
	// should remain the same
	modTime = time.Now().Add(-23 * time.Hour)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err = gw.Write(resp.Content)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	gzipBytes := buf.Bytes()
	respGzip := &httpserver.Response{
		Etag:            identity.NewID(),
		Content:         gzipBytes,
		LastModified:    &modTime,
		ContentEncoding: "gzip",
	}
	server.SetRoute("/foo", respGzip)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: tmpdir,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, 4, server.Stats("/foo").AllRequests)
	require.Equal(t, 1, server.Stats("/foo").CachedRequests)

	dt, err = os.ReadFile(filepath.Join(tmpdir, "foo"))
	require.NoError(t, err)
	require.Equal(t, resp.Content, dt)

	allReqs = server.Stats("/foo").Requests
	require.Equal(t, 4, len(allReqs))
	require.Equal(t, http.MethodHead, allReqs[2].Method)
	require.Equal(t, "gzip", allReqs[2].Header.Get("Accept-Encoding"))
	require.Equal(t, http.MethodGet, allReqs[3].Method)
	require.Equal(t, "gzip", allReqs[3].Header.Get("Accept-Encoding"))

	// test extra options
	// llb.Chown not supported on Windows
	st = integration.UnixOrWindows(
		llb.HTTP(server.URL+"/foo", llb.Filename("bar"), llb.Chmod(0741), llb.Chown(1000, 1000)),
		llb.HTTP(server.URL+"/foo", llb.Filename("bar"), llb.Chmod(0741)),
	)
	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: tmpdir,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, 5, server.Stats("/foo").AllRequests)
	require.Equal(t, 1, server.Stats("/foo").CachedRequests)

	dt, err = os.ReadFile(filepath.Join(tmpdir, "bar"))
	require.NoError(t, err)
	require.Equal(t, []byte("content1"), dt)

	fi, err := os.Stat(filepath.Join(tmpdir, "bar"))
	require.NoError(t, err)
	require.Equal(t, fi.ModTime().Format(http.TimeFormat), modTime.Format(http.TimeFormat))

	// no support for llb.Chmod on Windows, default is returned
	fMode := integration.UnixOrWindows(0741, 0666)
	require.Equal(t, fMode, int(fi.Mode()&0777))

	checkAllReleasable(t, c, sb, true)

	// TODO: check that second request was marked as cached
}

func testBuildHTTPSourceAuthHeaderSecret(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	modTime := time.Now().Add(-24 * time.Hour) // avoid false positive with current time

	resp := &httpserver.Response{
		Etag:         identity.NewID(),
		Content:      []byte("content1"),
		LastModified: &modTime,
	}

	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	st := llb.HTTP(server.URL+"/foo", llb.AuthHeaderSecret("http-secret"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(
		sb.Context(),
		def,
		SolveOpt{
			Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
				"http-secret": []byte("Bearer foo"),
			})},
		},
		nil,
	)
	require.NoError(t, err)

	allReqs := server.Stats("/foo").Requests
	require.Equal(t, 1, len(allReqs))
	require.Equal(t, http.MethodGet, allReqs[0].Method)
	require.Equal(t, "Bearer foo", allReqs[0].Header.Get("Authorization"))
}

// docker/buildx#2803
func testBuildHTTPSourceEtagScope(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	modTime := time.Now().Add(-24 * time.Hour) // avoid falso positive with current time

	sharedEtag := identity.NewID()
	resp := &httpserver.Response{
		Etag:         sharedEtag,
		Content:      []byte("content1"),
		LastModified: &modTime,
	}
	resp2 := &httpserver.Response{
		Etag:         sharedEtag,
		Content:      []byte("another"),
		LastModified: &modTime,
	}

	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/one/foo": resp,
		"/two/foo": resp2,
	})
	defer server.Close()

	// first correct request
	st := llb.HTTP(server.URL + "/one/foo")

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	out1 := t.TempDir()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: out1,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, 1, server.Stats("/one/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/one/foo").CachedRequests)

	dt, err := os.ReadFile(filepath.Join(out1, "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("content1"), dt)

	st = llb.HTTP(server.URL + "/two/foo")

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	out2 := t.TempDir()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: out2,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, 1, server.Stats("/two/foo").AllRequests)
	require.Equal(t, 0, server.Stats("/two/foo").CachedRequests)

	dt, err = os.ReadFile(filepath.Join(out2, "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("another"), dt)

	out2 = t.TempDir()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: out2,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, 2, server.Stats("/two/foo").AllRequests)
	require.Equal(t, 1, server.Stats("/two/foo").CachedRequests)

	allReqs := server.Stats("/two/foo").Requests
	require.Equal(t, 2, len(allReqs))
	require.Equal(t, http.MethodGet, allReqs[0].Method)
	require.Equal(t, "gzip", allReqs[0].Header.Get("Accept-Encoding"))
	require.Equal(t, http.MethodHead, allReqs[1].Method)
	require.Equal(t, "gzip", allReqs[1].Header.Get("Accept-Encoding"))

	require.NoError(t, os.RemoveAll(filepath.Join(out2, "foo")))
}

func testBuildHTTPSourceHeader(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	modTime := time.Now().Add(-24 * time.Hour) // avoid falso positive with current time

	resp := &httpserver.Response{
		Etag:         identity.NewID(),
		Content:      []byte("content1"),
		LastModified: &modTime,
	}

	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	st := llb.HTTP(
		server.URL+"/foo",
		llb.Header(llb.HTTPHeader{
			Accept:    "application/vnd.foo",
			UserAgent: "fooagent",
		}),
	)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	allReqs := server.Stats("/foo").Requests
	require.Equal(t, 1, len(allReqs))
	require.Equal(t, http.MethodGet, allReqs[0].Method)
	require.Equal(t, "application/vnd.foo", allReqs[0].Header.Get("accept"))
	require.Equal(t, "fooagent", allReqs[0].Header.Get("user-agent"))
}

func testBuildHTTPSourceHostTokenSecret(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	modTime := time.Now().Add(-24 * time.Hour) // avoid false positive with current time

	resp := &httpserver.Response{
		Etag:         identity.NewID(),
		Content:      []byte("content1"),
		LastModified: &modTime,
	}

	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	st := llb.HTTP(server.URL + "/foo")

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(
		sb.Context(),
		def,
		SolveOpt{
			Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
				"HTTP_AUTH_TOKEN_127.0.0.1": []byte("123456"),
			})},
		},
		nil,
	)
	require.NoError(t, err)

	allReqs := server.Stats("/foo").Requests
	require.Equal(t, 1, len(allReqs))
	require.Equal(t, http.MethodGet, allReqs[0].Method)
	require.Equal(t, "Bearer 123456", allReqs[0].Header.Get("Authorization"))
}

func testBuildHTTPSourcePGPSignatureVerify(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	signFixturesPath, ok := os.LookupEnv("BUILDKIT_TEST_SIGN_FIXTURES")
	if !ok {
		t.Skip("missing BUILDKIT_TEST_SIGN_FIXTURES")
	}

	payload, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.http.artifact"))
	require.NoError(t, err)
	sigData, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.http.artifact.asc"))
	require.NoError(t, err)
	pubKeyData, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.gpg.pub"))
	require.NoError(t, err)
	wrongPubKeyData, err := os.ReadFile(filepath.Join(signFixturesPath, "user2.gpg.pub"))
	require.NoError(t, err)

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artifact.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer httpSrv.Close()

	solve := func(t *testing.T, state llb.State) error {
		def, err := state.Marshal(sb.Context())
		require.NoError(t, err)
		tmpdir := t.TempDir()
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:      ExporterLocal,
					OutputDir: tmpdir,
				},
			},
		}, nil)
		if err != nil {
			return err
		}
		dt, err := os.ReadFile(filepath.Join(tmpdir, "artifact.txt"))
		require.NoError(t, err)
		require.Equal(t, payload, dt)
		return nil
	}

	t.Run("valid-signature", func(t *testing.T) {
		validState := llb.HTTP(
			httpSrv.URL+"/artifact.txt",
			llb.VerifyPGPSignature(llb.HTTPSignatureInfo{
				PubKey:    pubKeyData,
				Signature: sigData,
			}),
		)
		require.NoError(t, solve(t, validState))
	})

	t.Run("wrong-pubkey", func(t *testing.T) {
		invalidState := llb.HTTP(
			httpSrv.URL+"/artifact.txt",
			llb.VerifyPGPSignature(llb.HTTPSignatureInfo{
				PubKey:    wrongPubKeyData,
				Signature: sigData,
			}),
		)
		err = solve(t, invalidState)
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to verify pgp signature")
	})

	t.Run("concatenated-pubkeys-right-key-second", func(t *testing.T) {
		mergedPubKeys := append(append([]byte{}, wrongPubKeyData...), '\n')
		mergedPubKeys = append(mergedPubKeys, pubKeyData...)
		state := llb.HTTP(
			httpSrv.URL+"/artifact.txt",
			llb.VerifyPGPSignature(llb.HTTPSignatureInfo{
				PubKey:    mergedPubKeys,
				Signature: sigData,
			}),
		)
		require.NoError(t, solve(t, state))
	})
}

func testBuildHTTPSourceUnauthorizedChecksumRace(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureMergeDiff)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			time.Sleep(200 * time.Millisecond)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	url := server.URL + "/test.txt"
	lower := llb.Scratch()
	// Reproduce the race between same-URL HTTP sources with and without
	// checksum sharing the resolver cache while the server returns 401.
	withoutChecksum := lower.File(llb.Copy(llb.HTTP(url), "test.txt", "/a.txt"))
	withChecksum := lower.File(llb.Copy(llb.HTTP(url, llb.Checksum(digest.FromBytes(nil))), "test.txt", "/b.txt"))
	st := llb.Merge([]llb.State{
		llb.Diff(lower, withoutChecksum),
		llb.Diff(lower, withChecksum),
	})

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	for range 10 {
		requests.Store(0)
		_, err := c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type:      ExporterLocal,
					OutputDir: t.TempDir(),
				},
			},
		}, nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "invalid response status 401")
	}
}

func testHTTPPruneAfterCacheKey(t *testing.T, sb integration.Sandbox) {
	// this test depends on hitting race condition in internal functions.
	// If debugging and expecting failure you can add small sleep in beginning of source/http.Exec() to hit reliably
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	done := make(chan struct{})

	startScan := make(chan struct{})
	stopScan := make(chan struct{})
	pauseScan := make(chan struct{})

	go func() {
		// attempt to prune the HTTP record in between cachekey and snapshot
		defer close(done)
		for {
			select {
			case <-startScan:
			scan:
				for {
					select {
					case <-pauseScan:
						break scan
					default:
						du, err := c.DiskUsage(ctx)
						assert.NoError(t, err)
						for _, entry := range du {
							if entry.Description == "http url "+server.URL+"/foo" {
								if !entry.InUse {
									t.Logf("entry no longer in use, pruning")
									err = c.Prune(ctx, nil)
									assert.NoError(t, err)

									resp.Etag = identity.NewID()
									resp.Content = []byte("content2")
								}
							}
						}
					}
				}
			case <-stopScan:
				return
			}
		}
	}()

	const iterations = 10
	for range iterations {
		startScan <- struct{}{}
		resp.Etag = identity.NewID()
		resp.Content = []byte("content1")
		_, err = c.Build(ctx, SolveOpt{}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			st := llb.Scratch().File(llb.Copy(llb.HTTP(server.URL+"/foo"), "foo", "bar"))
			def, err := st.Marshal(sb.Context())
			if err != nil {
				return nil, err
			}
			resp, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, err
			}

			return resp, nil
		}, nil)
		require.NoError(t, err)

		pauseScan <- struct{}{}

		err = c.Prune(ctx, nil)
		require.NoError(t, err)

		checkAllReleasable(t, c, sb, false)
	}
	close(stopScan)
	<-done
}

// testHTTPPruneAfterResolveMeta ensures that pruning after ResolveSourceMetadata
// doesn't pull in new data for same build. Once URL has been resolved once for a specific
// build, the data should be considered immutable and remote changes don't affect ongoing build.
func testHTTPPruneAfterResolveMeta(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	dest := t.TempDir()

	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: dest,
			},
		},
	}, "test", func(ctx context.Context, gc gateway.Client) (*gateway.Result, error) {
		id := server.URL + "/foo"
		md, err := gc.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.HTTP)

		// prune all
		err = c.Prune(ctx, nil)
		require.NoError(t, err)

		resp.Content = []byte("content2") // etag is same so should hit cache if record not pruned

		st := llb.Scratch().File(llb.Copy(llb.HTTP(id), "foo", "bar"))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return gc.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(dest, "bar"))
	require.NoError(t, err)
	require.Equal(t, "content1", string(dt))

	checkAllReleasable(t, c, sb, false)
}

func testHTTPResolveMetaReuse(t *testing.T, sb integration.Sandbox) {
	// the difference with testHTTPPruneAfterResolveMeta is that here we change content with the etag on the server
	// but because the URL was already resolved once, the new content should not be seen
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	dest := t.TempDir()
	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: dest,
			},
		},
	}, "test", func(ctx context.Context, gc gateway.Client) (*gateway.Result, error) {
		id := server.URL + "/foo"
		md, err := gc.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.HTTP)

		resp.Etag = identity.NewID()
		resp.Content = []byte("content2") // etag changed so new content would be returned if re-resolving

		st := llb.Scratch().File(llb.Copy(llb.HTTP(id), "foo", "bar"))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return gc.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(dest, "bar"))
	require.NoError(t, err)
	require.Equal(t, "content1", string(dt))
}

// testHTTPResolveMultiBuild is a negative test for testHTTPResolveMetaReuse to ensure that
// URLs are resolved in between separate builds
func testHTTPResolveMultiBuild(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	resp := &httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	dest := t.TempDir()
	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: dest,
			},
		},
	}, "test", func(ctx context.Context, gc gateway.Client) (*gateway.Result, error) {
		id := server.URL + "/foo"
		md, err := gc.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.HTTP)
		require.Equal(t, digest.FromBytes(resp.Content), md.HTTP.Digest)

		st := llb.Scratch().File(llb.Copy(llb.HTTP(id), "foo", "bar"))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return gc.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(dest, "bar"))
	require.NoError(t, err)
	require.Equal(t, "content1", string(dt))

	resp.Etag = identity.NewID()
	resp.Content = []byte("content2")

	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: dest,
			},
		},
	}, "test", func(ctx context.Context, gc gateway.Client) (*gateway.Result, error) {
		id := server.URL + "/foo"
		md, err := gc.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.HTTP)
		require.Equal(t, digest.FromBytes(resp.Content), md.HTTP.Digest)
		st := llb.Scratch().File(llb.Copy(llb.HTTP(id), "foo", "bar"))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return gc.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(dest, "bar"))
	require.NoError(t, err)
	require.Equal(t, "content2", string(dt))
}

func testHTTPResolveSourceMetadata(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	modTime := time.Now().Add(-24 * time.Hour) // avoid falso positive with current time

	resp := &httpserver.Response{
		Etag:         identity.NewID(),
		Content:      []byte("content1"),
		LastModified: &modTime,
	}

	resp2 := &httpserver.Response{
		Etag:               identity.NewID(),
		Content:            []byte("content2"),
		ContentDisposition: "attachment; filename=\"my img.jpg\"",
	}

	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/foo": resp,
		"/bar": resp2,
	})
	defer server.Close()

	_, err = c.Build(ctx, SolveOpt{}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		id := server.URL + "/foo"
		md, err := c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.HTTP)
		require.Equal(t, digest.FromBytes(resp.Content), md.HTTP.Digest)
		require.Equal(t, "foo", md.HTTP.Filename)
		require.NotNil(t, md.HTTP.LastModified)
		require.Equal(t, modTime.Unix(), md.HTTP.LastModified.Unix())
		require.Equal(t, id, md.Op.Identifier)

		id = server.URL + "/bar"
		md, err = c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.HTTP)
		require.Equal(t, digest.FromBytes(resp2.Content), md.HTTP.Digest)
		require.Equal(t, "my img.jpg", md.HTTP.Filename)
		require.Nil(t, md.HTTP.LastModified)
		require.Equal(t, id, md.Op.Identifier)
		return nil, nil
	}, nil)
	require.NoError(t, err)
}
