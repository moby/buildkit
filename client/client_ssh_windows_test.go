package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/sshutil"
	"github.com/moby/buildkit/util/testutil/sshutil/probe"
	"github.com/stretchr/testify/require"
)

func init() {
	allTests = append(allTests, testSSHMountWindows)
}

func testSSHMountWindows(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	workers, err := c.ListWorkers(sb.Context())
	require.NoError(t, err)
	require.NotEmpty(t, workers)
	require.NotEmpty(t, workers[0].Platforms)
	target := workers[0].Platforms[0]
	require.Equal(t, "windows", target.OS, "SSH tests require a Windows worker")
	binary := sshutil.BuildProbe(t, target.Architecture)
	base := llb.Image("nanoserver:latest", llb.Platform(target)).
		File(llb.Mkfile("/sshprobe.exe", 0755, binary)).
		User("ContainerAdministrator")
	for _, tc := range []struct {
		name        string
		provider    bool
		id          string
		exposeID    bool
		optional    bool
		keyFile     bool
		mutate      bool
		connections int
		wantError   string
	}{
		{name: "required-no-provider", wantError: "no SSH key "},
		{name: "required-missing-id", provider: true, id: "customID", wantError: "unset ssh forward key customID"},
		{name: "optional-no-provider", optional: true},
		{name: "optional-missing-id", provider: true, id: "customID", optional: true},
		{name: "agent-identity", provider: true, connections: 1},
		{name: "key-file-identity", provider: true, keyFile: true, connections: 1},
		{name: "custom-id-identity", provider: true, id: "customID", exposeID: true, connections: 1},
		{name: "agent-read-only", provider: true, mutate: true, connections: 1},
		{name: "connection-lifecycle", provider: true, connections: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := sshutil.NewAgent(t)
			if tc.connections > 1 {
				a.RequireSequentialConnections()
			}
			var attachables []session.Attachable
			if tc.provider {
				path := a.Endpoint
				if tc.keyFile {
					path = a.KeyFile
				}
				configs := []sshprovider.AgentConfig{{Paths: []string{path}}}
				if tc.exposeID {
					// A different default key makes selecting the wrong ID observable.
					other := sshutil.NewAgent(t)
					configs = []sshprovider.AgentConfig{
						{Paths: []string{other.Endpoint}},
						{ID: tc.id, Paths: []string{path}},
					}
				}
				provider, err := sshprovider.NewSSHAgentProvider(configs)
				require.NoError(t, err)
				attachables = []session.Attachable{provider}
			}
			opts := []llb.SSHOption{llb.SSHID(tc.id)}
			if tc.optional {
				opts = append(opts, llb.SSHOptional)
			}
			args := []string{`C:\sshprobe.exe`, "-output", `C:\ssh-report.json`}
			if tc.optional {
				args = append(args, "-absent")
			} else {
				args = append(args, "-expected", a.PublicKey, "-cycles", fmt.Sprint(tc.connections))
			}
			if tc.mutate {
				args = append(args, "-mutate")
			}
			run := base.Run(llb.Args(args), llb.AddSSHSocket(opts...), llb.IgnoreCache)
			out := llb.Scratch().File(llb.Copy(run.Root(), "/ssh-report.json", "/ssh-report.json"))
			def, err := out.Marshal(sb.Context())
			require.NoError(t, err)
			dest := sshutil.WorkDir(t)
			solve := func() error {
				_, err := c.Solve(sb.Context(), def, SolveOpt{
					Session: attachables,
					Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: dest}},
				}, nil)
				return err
			}
			err = solve()
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.NotContains(t, err.Error(), "did not complete successfully")
				return
			}
			require.NoError(t, err)
			dt, err := os.ReadFile(filepath.Join(dest, "ssh-report.json"))
			require.NoError(t, err)
			var report probe.Report
			require.NoError(t, json.Unmarshal(dt, &report))
			require.Equal(t, tc.optional, report.Absent)
			require.Equal(t, tc.connections, report.Connections)
			if !tc.optional {
				require.Equal(t, []string{a.PublicKey}, report.Keys)
				require.Equal(t, tc.mutate, report.AddRejected)
				require.Equal(t, tc.mutate, report.RemoveAllRejected)
			}
			a.CheckKey(t)
			if tc.provider && !tc.keyFile && !tc.optional {
				a.WaitIdle(t, tc.connections)
				if tc.connections > 1 {
					require.NoError(t, solve(), "connections must still work after disconnect")
					a.WaitIdle(t, 2*tc.connections)
				}
			}
		})
	}
}
