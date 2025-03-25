package nvidia

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// This is example of experimental on-demand setup of a CDI devices.
// This code is not currently shipping with BuildKit and will probably change.

const cdiKind = "nvidia.com/gpu"

func init() {
	cdidevices.Register(cdiKind, &setup{})
}

type setup struct{}

var _ cdidevices.Setup = &setup{}

func (s *setup) Validate() error {
	b, err := hasNvidiaDevices()
	if err != nil {
		return err
	}
	if !b {
		return errors.Errorf("no NVIDIA devices found")
	}
	return nil
}

func newVertex(ctx context.Context, name string) (progress.Writer, digest.Digest, func(error)) {
	pw, _, ctx := progress.NewFromContext(ctx)
	start := time.Now()
	id := identity.NewID()
	v := &client.Vertex{
		Name:   name,
		Digest: digest.FromBytes([]byte(id)),
	}
	v.Started = &start
	v.Completed = nil
	v.Cached = false
	pw.Write(id, *v)

	pw2, _, _ := progress.NewFromContext(ctx, progress.WithMetadata("vertex", v.Digest))

	return pw2, v.Digest, func(err error) {
		pw2.Close()
		stop := time.Now()
		v.Completed = &stop
		if err != nil {
			v.Error = err.Error()
		} else {
			v.Error = ""
		}
		pw.Write(id, *v)
		pw.Close()
	}
}

func (s *setup) Run(ctx context.Context) (err error) {
	pw, dgst, closeProgress := newVertex(ctx, fmt.Sprintf("preparing device %s", cdiKind))
	defer func() {
		closeProgress(err)
	}()

	isDistro, _ := isDebianOrUbuntu()
	if !isDistro {
		return errors.Errorf("NVIDIA setup is currently only supported on Debian/Ubuntu")
	}

	if err := run(ctx, []string{"apt-get", "update"}, pw, dgst); err != nil {
		return err
	}

	if err := run(ctx, []string{"apt-get", "install", "-y", "gpg"}, pw, dgst); err != nil {
		return err
	}

	aptURL := "https://nvidia.github.io/libnvidia-container/"
	keyTarget := "/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg"

	if _, err := os.Stat(keyTarget); err != nil {
		fmt.Fprintf(newStream(pw, 2, dgst), "Downloading NVIDIA GPG key\n")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, aptURL+"gpgkey", nil)
		if err != nil {
			return errors.Wrapf(err, "failed to create request for NVIDIA GPG key")
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return errors.Wrapf(err, "failed to download NVIDIA GPG key")
		}

		cmd := exec.CommandContext(ctx, "gpg", "--dearmor", "-o", keyTarget)
		cmd.Stdin = resp.Body
		cmd.Stderr = newStream(pw, 2, dgst)
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "failed to install NVIDIA GPG key")
		}
		resp.Body.Close()
	}

	if err := os.WriteFile("/etc/apt/sources.list.d/nvidia-container-toolkit.list", []byte("deb [signed-by="+keyTarget+"] "+aptURL+"stable/deb/$(ARCH) /"), 0644); err != nil {
		return errors.Wrapf(err, "failed to add NVIDIA apt repo")
	}

	if err := run(ctx, []string{"apt-get", "update"}, pw, dgst); err != nil {
		return err
	}

	if err := run(ctx, []string{"apt-get", "install", "-y", "nvidia-container-toolkit-base"}, pw, dgst); err != nil {
		return err
	}

	if err := os.MkdirAll("/etc/cdi", 0700); err != nil {
		return errors.Wrapf(err, "failed to create /etc/cdi")
	}

	buf := &bytes.Buffer{}

	cmd := exec.CommandContext(ctx, "nvidia-ctk", "cdi", "generate")
	cmd.Stdout = buf
	cmd.Stderr = newStream(pw, 2, dgst)
	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "failed to generate CDI spec")
	}

	if len(buf.Bytes()) == 0 {
		return errors.Errorf("nvidia-ctk output is empty")
	}

	if err := os.WriteFile("/etc/cdi/nvidia.yaml", buf.Bytes(), 0644); err != nil {
		return errors.Wrapf(err, "failed to write /etc/cdi/nvidia.yaml")
	}

	return nil
}

func run(ctx context.Context, args []string, pw progress.Writer, dgst digest.Digest) error {
	fmt.Fprintf(newStream(pw, 2, dgst), "> %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec
	cmd.Stderr = newStream(pw, 2, dgst)
	cmd.Stdout = newStream(pw, 1, dgst)
	return cmd.Run()
}

func hasNvidiaDevices() (bool, error) {
	const pciDevicesPath = "/sys/bus/pci/devices"
	const nvidiaVendorID = "0x10de"

	found := false

	dirs, err := os.ReadDir(pciDevicesPath)
	if err != nil {
		return false, err
	}

	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(pciDevicesPath, dir.Name(), "vendor"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == nvidiaVendorID {
			found = true
			break
		}
	}

	return found, nil
}

func getOSID() (string, error) {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ID=") {
			id := strings.TrimPrefix(line, "ID=")
			return strings.Trim(id, `"`), nil // Remove potential quotes
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return "", errors.Errorf("ID not found in /etc/os-release")
}

func isDebianOrUbuntu() (bool, error) {
	id, err := getOSID()
	if err != nil {
		return false, err
	}

	return id == "debian" || id == "ubuntu", nil
}
