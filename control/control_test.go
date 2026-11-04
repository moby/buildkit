package control

import (
	"context"
	"maps"
	"testing"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/cache/remotecache"
	"github.com/moby/buildkit/cache/remotecache/inline"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/stretchr/testify/require"
)

func TestDuplicateCacheOptions(t *testing.T) {
	var testCases = []struct {
		name     string
		opts     []*controlapi.CacheOptionsEntry
		expected []*controlapi.CacheOptionsEntry
		rest     []*controlapi.CacheOptionsEntry
	}{
		{
			name: "avoids unique opts",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
			},
			expected: nil,
		},
		{
			name: "finds duplicate opts",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
			},
			expected: []*controlapi.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
			},
		},
		{
			name: "skip inline with attrs",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "inline",
				},
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "inline",
					Attrs: map[string]string{
						"foo": "bar",
					},
				},
			},
			rest: []*controlapi.CacheOptionsEntry{
				{
					Type: "inline",
				},
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
			},
			expected: nil,
		},
		{
			name: "skip inline simple",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "inline",
				},
				{
					Type: "inline",
				},
			},
			rest: []*controlapi.CacheOptionsEntry{
				{
					Type: "inline",
				},
			},
			expected: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rest, result, err := findDuplicateCacheOptions(tc.opts)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.expected, result)
			if tc.rest != nil {
				require.ElementsMatch(t, tc.rest, rest)
			} else if len(result) == 0 {
				require.ElementsMatch(t, tc.opts, rest)
			}
		})
	}
}

func TestParseCacheExportIgnoreError(t *testing.T) {
	tests := map[string]struct {
		expectedIgnoreError bool
		expectedSupported   bool
	}{
		"": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
		".": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
		"fake": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
		"true": {
			expectedIgnoreError: true,
			expectedSupported:   true,
		},
		"True": {
			expectedIgnoreError: true,
			expectedSupported:   true,
		},
		"TRUE": {
			expectedIgnoreError: true,
			expectedSupported:   true,
		},
		"truee": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
		"false": {
			expectedIgnoreError: false,
			expectedSupported:   true,
		},
		"False": {
			expectedIgnoreError: false,
			expectedSupported:   true,
		},
		"FALSE": {
			expectedIgnoreError: false,
			expectedSupported:   true,
		},
		"ffalse": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
	}

	for ignoreErrStr, test := range tests {
		t.Run(ignoreErrStr, func(t *testing.T) {
			ignoreErr, supported := parseCacheExportIgnoreError(ignoreErrStr)
			t.Log("checking expectedIgnoreError")
			require.Equal(t, test.expectedIgnoreError, ignoreErr)
			t.Log("checking expectedSupported")
			require.Equal(t, test.expectedSupported, supported)
		})
	}
}

// TestCacheExporterCompression checks common options independently of backend setup.
func TestCacheExporterCompression(t *testing.T) {
	tests := []struct {
		name    string
		attrs   map[string]string
		want    compression.Config
		wantErr string
	}{
		{
			name:  "default",
			attrs: map[string]string{},
			want:  compression.New(compression.Default),
		},
		{
			name:  "gzip",
			attrs: map[string]string{"compression": "gzip"},
			want:  compression.New(compression.Gzip),
		},
		{
			name:  "zstd",
			attrs: map[string]string{"compression": "zstd"},
			want:  compression.New(compression.Zstd),
		},
		{
			name:  "uncompressed",
			attrs: map[string]string{"compression": "uncompressed"},
			want:  compression.New(compression.Uncompressed),
		},
		{
			name:  "estargz",
			attrs: map[string]string{"compression": "estargz"},
			want:  compression.New(compression.EStargz),
		},
		{
			name:  "level",
			attrs: map[string]string{"compression": "zstd", "compression-level": "12"},
			want:  compression.New(compression.Zstd).SetLevel(12),
		},
		{
			name:  "force",
			attrs: map[string]string{"compression": "zstd", "force-compression": "true"},
			want:  compression.New(compression.Zstd).SetForce(true),
		},
		{
			name:  "force without value",
			attrs: map[string]string{"force-compression": ""},
			want:  compression.New(compression.Default).SetForce(true),
		},
		{
			name:    "unknown compression type",
			attrs:   map[string]string{"compression": "lzma"},
			wantErr: "unsupported compression type lzma",
		},
		{
			name:    "non-integer compression level",
			attrs:   map[string]string{"compression-level": "fastest"},
			wantErr: "non-integer value fastest specified for compression-level",
		},
		{
			name:    "non-bool force compression",
			attrs:   map[string]string{"force-compression": "yes please"},
			wantErr: "non-bool value yes please specified for force-compression",
		},
	}

	// An unrecognized backend name demonstrates that new backends inherit these options.
	for _, backend := range []string{"registry", "local", "s3", "gha", "azblob", "custom"} {
		t.Run(backend, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					attrs := maps.Clone(tt.attrs)
					attrs["mode"] = "max"
					attrs["ignore-error"] = "true"
					called := false
					backendExporter := remotecache.NewExporter(nil, "", true, true)
					c := &Controller{opt: Opt{
						ResolveCacheExporterFuncs: map[string]remotecache.ResolveCacheExporterFunc{
							backend: func(ctx context.Context, g session.Group, got map[string]string) (remotecache.Exporter, error) {
								called = true
								require.Equal(t, attrs, got)
								return backendExporter, nil
							},
						},
					}}
					exporters, err := c.resolveCacheExporters(t.Context(), nil, []*controlapi.CacheOptionsEntry{
						{
							Type:  backend,
							Attrs: attrs,
						},
					})
					if tt.wantErr != "" {
						require.ErrorContains(t, err, tt.wantErr)
						require.False(t, called, "invalid compression must fail before backend setup, even with ignore-error")
						return
					}
					require.NoError(t, err)
					require.Len(t, exporters, 1)
					require.Same(t, backendExporter, exporters[0].Exporter)
					require.Equal(t, tt.want, exporters[0].Compression)
					require.Equal(t, solver.CacheExportModeMax, exporters[0].CacheExportMode)
					require.True(t, exporters[0].IgnoreError)
				})
			}
		})
	}
}

func TestInlineCacheIgnoresCompressionAttributes(t *testing.T) {
	c := &Controller{opt: Opt{
		ResolveCacheExporterFuncs: map[string]remotecache.ResolveCacheExporterFunc{
			"inline": inline.ResolveCacheExporterFunc(),
		},
	}}
	exporters, err := c.resolveCacheExporters(t.Context(), nil, []*controlapi.CacheOptionsEntry{
		{
			Type: "inline",
			Attrs: map[string]string{
				"compression":       "invalid",
				"compression-level": "invalid",
				"force-compression": "invalid",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, exporters, 1)
	require.Equal(t, compression.Config{}, exporters[0].Compression)
}
