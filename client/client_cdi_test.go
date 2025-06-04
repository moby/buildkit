package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/stretchr/testify/require"
)

var cdiTests = []func(t *testing.T, sb integration.Sandbox){
	testCDI,
	testCDINotAllowed,
	testCDIEntitlement,
	testCDIFirst,
	testCDIWildcard,
	testCDIClass,
}

func testCDI(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCDI)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, os.WriteFile(filepath.Join(sb.CDISpecDir(), "vendor1-device.yaml"), []byte(`
cdiVersion: "0.6.0"
kind: "vendor1.com/device"
devices:
- name: foo
  containerEdits:
    env:
    - FOO=injected
annotations:
  org.mobyproject.buildkit.device.autoallow: true
`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sb.CDISpecDir(), "vendor2-device.yaml"), []byte(`
cdiVersion: "0.6.0"
kind: "vendor2.com/device"
devices:
- name: bar
  containerEdits:
    env:
    - BAR=injected
annotations:
  org.mobyproject.buildkit.device.autoallow: true
`), 0600))

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = busybox.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c 'env|sort | tee foo.env'`, llb.AddCDIDevice(llb.CDIDeviceName("vendor1.com/device=foo")))
	run(`sh -c 'env|sort | tee bar.env'`, llb.AddCDIDevice(llb.CDIDeviceName("vendor2.com/device=bar")))
	run(`ls`, llb.AddCDIDevice(llb.CDIDeviceName("vendor3.com/device=baz"), llb.CDIDeviceOptional))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo.env"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), `FOO=injected`)

	dt2, err := os.ReadFile(filepath.Join(destDir, "bar.env"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt2)), `BAR=injected`)
}

func testCDINotAllowed(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCDI)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, os.WriteFile(filepath.Join(sb.CDISpecDir(), "vendor1-device.yaml"), []byte(`
cdiVersion: "0.6.0"
kind: "vendor1.com/device"
devices:
- name: foo
  containerEdits:
    env:
    - FOO=injected
`), 0600))

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = busybox.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c 'env|sort | tee foo.env'`, llb.AddCDIDevice(llb.CDIDeviceName("vendor1.com/device=foo")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "requested by the build but not allowed")
}

func testCDIEntitlement(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCDI)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, os.WriteFile(filepath.Join(sb.CDISpecDir(), "vendor1-device.yaml"), []byte(`
cdiVersion: "0.6.0"
kind: "vendor1.com/device"
devices:
- name: foo
  containerEdits:
    env:
    - FOO=injected
`), 0600))

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = busybox.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c 'env|sort | tee foo.env'`, llb.AddCDIDevice(llb.CDIDeviceName("vendor1.com/device=foo")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		AllowedEntitlements: []string{"device=vendor1.com/device"},
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo.env"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), `FOO=injected`)
}

func testCDIFirst(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCDI)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, os.WriteFile(filepath.Join(sb.CDISpecDir(), "vendor1-device.yaml"), []byte(`
cdiVersion: "0.6.0"
kind: "vendor1.com/device"
devices:
- name: foo
  containerEdits:
    env:
    - FOO=injected
- name: bar
  containerEdits:
    env:
    - BAR=injected
- name: baz
  containerEdits:
    env:
    - BAZ=injected
- name: qux
  containerEdits:
    env:
    - QUX=injected
annotations:
  org.mobyproject.buildkit.device.autoallow: true
`), 0600))

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = busybox.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c 'env|sort | tee first.env'`, llb.AddCDIDevice(llb.CDIDeviceName("vendor1.com/device")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "first.env"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), `BAR=injected`)
	require.NotContains(t, strings.TrimSpace(string(dt)), `FOO=injected`)
	require.NotContains(t, strings.TrimSpace(string(dt)), `BAZ=injected`)
	require.NotContains(t, strings.TrimSpace(string(dt)), `QUX=injected`)
}

func testCDIWildcard(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCDI)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, os.WriteFile(filepath.Join(sb.CDISpecDir(), "vendor1-device.yaml"), []byte(`
cdiVersion: "0.6.0"
kind: "vendor1.com/device"
devices:
- name: foo
  containerEdits:
    env:
    - FOO=injected
- name: bar
  containerEdits:
    env:
    - BAR=injected
annotations:
  org.mobyproject.buildkit.device.autoallow: true
`), 0600))

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = busybox.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c 'env|sort | tee all.env'`, llb.AddCDIDevice(llb.CDIDeviceName("vendor1.com/device=*")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "all.env"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), `FOO=injected`)
	require.Contains(t, strings.TrimSpace(string(dt)), `BAR=injected`)
}

func testCDIClass(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCDI)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, os.WriteFile(filepath.Join(sb.CDISpecDir(), "vendor1-device.yaml"), []byte(`
cdiVersion: "0.6.0"
kind: "vendor1.com/device"
annotations:
  foo.bar.baz: FOO
  org.mobyproject.buildkit.device.autoallow: true
devices:
- name: foo
  annotations:
    org.mobyproject.buildkit.device.class: class1
  containerEdits:
    env:
    - FOO=injected
- name: bar
  annotations:
    org.mobyproject.buildkit.device.class: class1
  containerEdits:
    env:
    - BAR=injected
- name: baz
  annotations:
    org.mobyproject.buildkit.device.class: class2
  containerEdits:
    env:
    - BAZ=injected
- name: qux
  containerEdits:
    env:
    - QUX=injected
`), 0600))

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = busybox.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c 'env|sort | tee class.env'`, llb.AddCDIDevice(llb.CDIDeviceName("class1")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "class.env"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), `FOO=injected`)
	require.Contains(t, strings.TrimSpace(string(dt)), `BAR=injected`)
	require.NotContains(t, strings.TrimSpace(string(dt)), `BAZ=injected`)
	require.NotContains(t, strings.TrimSpace(string(dt)), `QUX=injected`)
}
