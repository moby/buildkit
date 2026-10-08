package dockerfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/sshutil"
	"github.com/moby/buildkit/util/testutil/sshutil/probe"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(testSSHWindowsAgentPipe)...)
}

func testSSHWindowsAgentPipe(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	workers, err := c.ListWorkers(sb.Context())
	require.NoError(t, err)
	require.NotEmpty(t, workers)
	require.NotEmpty(t, workers[0].Platforms)
	target := workers[0].Platforms[0]
	require.Equal(t, "windows", target.OS, "SSH tests require a Windows worker")
	binary := sshutil.BuildProbe(t, target.Architecture)
	openSSHCheck := sshutil.BuildOpenSSHCheck(t, target.Architecture)
	openSSHClient, openSSHLibCrypto := sshutil.OpenSSHRuntime(t, target.Architecture)
	for _, tc := range []struct {
		name     string
		provider bool
		keyFile  bool
		optional bool
		cycles   int
		target   string
		user     string
		output   string
		openSSH  bool
	}{
		{name: "required-no-provider"},
		{name: "optional-no-provider", optional: true},
		{name: "agent-identity-read-only", provider: true, cycles: 1},
		{name: "key-file-identity", provider: true, keyFile: true, cycles: 1},
		{name: "connection-lifecycle", provider: true, cycles: 5},
		{name: "custom-target", provider: true, cycles: 1, target: `\\.\PIPE\custom-agent`, openSSH: true},
		{name: "container-user", provider: true, cycles: 1, user: "ContainerUser", output: `C:\Users\ContainerUser\report.json`},
		{name: "container-user-openssh", provider: true, cycles: 1, target: `\\.\PIPE\custom-agent`, user: "ContainerUser", output: `C:\Users\ContainerUser\report.json`, openSSH: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := sshutil.NewAgent(t)
			if tc.cycles > 1 {
				a.RequireSequentialConnections()
			}
			var attachables []session.Attachable
			if tc.provider {
				path := a.Endpoint
				if tc.keyFile {
					path = a.KeyFile
				}
				provider, err := sshprovider.NewSSHAgentProvider([]sshprovider.AgentConfig{{Paths: []string{path}}})
				require.NoError(t, err)
				attachables = []session.Attachable{provider}
			}
			user := tc.user
			if user == "" {
				user = "ContainerAdministrator"
			}
			output := tc.output
			if output == "" {
				output = `C:\out\report.json`
			}
			args := []string{`C:\sshprobe.exe`, "-output", output}
			if tc.openSSH {
				args = []string{`C:\opensshcheck.exe`, "-ssh", `C:\ssh.exe`, "-expected", a.PublicKey, "-output", output}
			} else if tc.optional {
				args = append(args, "-absent")
			} else {
				args = append(args, "-expected", a.PublicKey, "-cycles", fmt.Sprint(tc.cycles))
				if tc.provider && !tc.keyFile {
					args = append(args, "-mutate")
				}
			}
			command, err := json.Marshal(args)
			require.NoError(t, err)
			mount := "--mount=type=ssh"
			if tc.target != "" {
				mount += ",target=" + tc.target
			}
			copyTools := "COPY sshprobe.exe C:/sshprobe.exe"
			contextFiles := []fstest.Applier{
				fstest.CreateFile("sshprobe.exe", binary, 0755),
			}
			if tc.openSSH {
				copyTools = "COPY opensshcheck.exe C:/opensshcheck.exe\nCOPY ssh.exe C:/ssh.exe\nCOPY libcrypto.dll C:/libcrypto.dll"
				contextFiles = append(contextFiles,
					fstest.CreateFile("opensshcheck.exe", openSSHCheck, 0755),
					fstest.CreateFile("ssh.exe", openSSHClient, 0755),
					fstest.CreateFile("libcrypto.dll", openSSHLibCrypto, 0755),
				)
			}
			copySource := strings.ReplaceAll(output, `\`, "/")
			dockerfile := "# escape=`\n" + fmt.Sprintf(`FROM nanoserver AS test
%s
USER ContainerAdministrator
RUN mkdir C:\out
USER %s
RUN %s,required=%t %s
FROM scratch
COPY --from=test %s /report.json
`, copyTools, user, mount, !tc.optional, command, copySource)
			// Only test executables enter the context; the private key remains on the host.
			contextFiles = append(contextFiles, fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600))
			dir := integration.Tmpdir(t, contextFiles...)
			dest := sshutil.WorkDir(t)
			solve := func() error {
				_, err := f.Solve(sb.Context(), c, client.SolveOpt{
					FrontendAttrs: map[string]string{"no-cache": "", "platform": platforms.Format(target)},
					LocalMounts: map[string]fsutil.FS{
						dockerui.DefaultLocalNameDockerfile: dir,
						dockerui.DefaultLocalNameContext:    dir,
					},
					Session: attachables,
					Exports: []client.ExportEntry{{Type: client.ExporterLocal, OutputDir: dest}},
				}, nil)
				return err
			}
			err = solve()
			if !tc.provider && !tc.optional {
				require.ErrorContains(t, err, `no SSH key "" forwarded from the client`)
				require.NotContains(t, err.Error(), "did not complete successfully")
				return
			}
			require.NoError(t, err)
			dt, err := os.ReadFile(filepath.Join(dest, "report.json"))
			require.NoError(t, err)
			if tc.openSSH {
				require.Equal(t, "authenticated with Windows OpenSSH\n", string(dt))
				a.CheckKey(t)
				a.WaitIdle(t, 1)
				return
			}
			var report probe.Report
			require.NoError(t, json.Unmarshal(dt, &report))
			require.Equal(t, tc.optional, report.Absent)
			require.Equal(t, tc.cycles, report.Connections)
			if tc.target != "" {
				require.Equal(t, tc.target, report.Endpoint)
				require.Equal(t, tc.target, report.AuthSock)
			}
			if tc.provider {
				require.Equal(t, []string{a.PublicKey}, report.Keys)
				require.Equal(t, !tc.keyFile, report.AddRejected)
				require.Equal(t, !tc.keyFile, report.RemoveAllRejected)
				a.CheckKey(t)
				if !tc.keyFile {
					a.WaitIdle(t, tc.cycles)
					if tc.cycles > 1 {
						require.NoError(t, solve(), "connections must still work after disconnect")
						a.WaitIdle(t, 2*tc.cycles)
					}
				}
			}
		})
	}
}
