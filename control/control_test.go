package control

import (
	"context"
	"testing"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/solver/llbsolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockExporterInstance is a minimal ExporterInstance for testing resolveEagerExport.
type mockExporterInstance struct {
	typ   string
	attrs map[string]string
}

func (m *mockExporterInstance) ID() int                  { return 0 }
func (m *mockExporterInstance) Name() string             { return "mock" }
func (m *mockExporterInstance) Config() *exporter.Config { return exporter.NewConfig() }
func (m *mockExporterInstance) Type() string             { return m.typ }
func (m *mockExporterInstance) Attrs() map[string]string { return m.attrs }
func (m *mockExporterInstance) Export(context.Context, *exporter.Source, exptypes.InlineCache, string) (map[string]string, exporter.DescriptorReference, error) {
	return nil, nil, nil
}

// mockEagerExporterInstance also implements EagerExportProvider.
type mockEagerExporterInstance struct {
	mockExporterInstance
	pushCfg *exporter.EagerPushConfig
}

func (m *mockEagerExporterInstance) EagerPushConfig() *exporter.EagerPushConfig {
	return m.pushCfg
}

func TestDuplicateCacheOptions(t *testing.T) {
	var testCases = []struct {
		name     string
		opts     []*controlapi.CacheOptionsEntry
		expected []*controlapi.CacheOptionsEntry
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
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := findDuplicateCacheOptions(tc.opts)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.expected, result)
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

func TestResolveEagerExport(t *testing.T) {
	imageExporter := &mockEagerExporterInstance{
		mockExporterInstance: mockExporterInstance{typ: client.ExporterImage},
		pushCfg: &exporter.EagerPushConfig{
			TargetName: "docker.io/library/test:latest",
		},
	}

	tests := []struct {
		name        string
		exporters   []*controlapi.Exporter
		instances   []exporter.ExporterInstance
		wantMode    llbsolver.EagerExportMode
		wantPushCfg bool
		wantErr     string
	}{
		{
			name:      "no eager-export attr returns none",
			exporters: []*controlapi.Exporter{{Attrs: map[string]string{}}},
			instances: []exporter.ExporterInstance{imageExporter},
			wantMode:  llbsolver.EagerExportNone,
		},
		{
			name: "compress mode",
			exporters: []*controlapi.Exporter{{
				Attrs: map[string]string{
					string(exptypes.OptKeyEagerExport): exptypes.OptValEagerExportCompress,
				},
			}},
			instances: []exporter.ExporterInstance{imageExporter},
			wantMode:  llbsolver.EagerExportCompress,
		},
		{
			name: "push mode with push=true",
			exporters: []*controlapi.Exporter{{
				Attrs: map[string]string{
					string(exptypes.OptKeyEagerExport): exptypes.OptValEagerExportPush,
					string(exptypes.OptKeyPush):        "true",
				},
			}},
			instances:   []exporter.ExporterInstance{imageExporter},
			wantMode:    llbsolver.EagerExportPush,
			wantPushCfg: true,
		},
		{
			name: "push mode without push=true errors",
			exporters: []*controlapi.Exporter{{
				Attrs: map[string]string{
					string(exptypes.OptKeyEagerExport): exptypes.OptValEagerExportPush,
				},
			}},
			instances: []exporter.ExporterInstance{imageExporter},
			wantErr:   "eager-export=push requires push=true",
		},
		{
			name: "invalid eager-export value errors",
			exporters: []*controlapi.Exporter{{
				Attrs: map[string]string{
					string(exptypes.OptKeyEagerExport): "bogus",
				},
			}},
			instances: []exporter.ExporterInstance{imageExporter},
			wantErr:   "invalid eager-export value",
		},
		{
			name: "non-image exporter errors",
			exporters: []*controlapi.Exporter{{
				Attrs: map[string]string{
					string(exptypes.OptKeyEagerExport): exptypes.OptValEagerExportCompress,
				},
			}},
			instances: []exporter.ExporterInstance{
				&mockExporterInstance{typ: "local"},
			},
			wantErr: "eager-export requires image exporter",
		},
		{
			name: "multiple exporters with eager-export errors",
			exporters: []*controlapi.Exporter{
				{Attrs: map[string]string{string(exptypes.OptKeyEagerExport): "compress"}},
				{Attrs: map[string]string{}},
			},
			instances: []exporter.ExporterInstance{imageExporter, imageExporter},
			wantErr:   "eager-export requires exactly one exporter",
		},
		{
			name:      "multiple exporters without eager-export is fine",
			exporters: []*controlapi.Exporter{{Attrs: map[string]string{}}, {Attrs: map[string]string{}}},
			instances: []exporter.ExporterInstance{imageExporter, imageExporter},
			wantMode:  llbsolver.EagerExportNone,
		},
		{
			name:      "zero exporters returns none",
			exporters: nil,
			instances: nil,
			wantMode:  llbsolver.EagerExportNone,
		},
		{
			name: "push mode with nil EagerPushConfig errors",
			exporters: []*controlapi.Exporter{{
				Attrs: map[string]string{
					string(exptypes.OptKeyEagerExport): exptypes.OptValEagerExportPush,
					string(exptypes.OptKeyPush):        "true",
				},
			}},
			instances: []exporter.ExporterInstance{
				&mockEagerExporterInstance{
					mockExporterInstance: mockExporterInstance{typ: client.ExporterImage},
					pushCfg:              nil,
				},
			},
			wantErr: "eager-export=push requires a single image name",
		},
		{
			name: "push mode with non-EagerExportProvider exporter errors",
			exporters: []*controlapi.Exporter{{
				Attrs: map[string]string{
					string(exptypes.OptKeyEagerExport): exptypes.OptValEagerExportPush,
					string(exptypes.OptKeyPush):        "true",
				},
			}},
			instances: []exporter.ExporterInstance{
				&mockExporterInstance{typ: client.ExporterImage},
			},
			wantErr: "exporter does not support eager push",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, pushCfg, err := resolveEagerExport(tt.exporters, tt.instances)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMode, mode)
			if tt.wantPushCfg {
				assert.NotNil(t, pushCfg)
			} else {
				assert.Nil(t, pushCfg)
			}
		})
	}
}
