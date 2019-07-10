package netns_create

import (
	"log"
	"os"
	"os/exec"
	"syscall"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

const envKey = "BUILDKIT_CREATE_NS_PATH"

func Handle() {
	if path := os.Getenv(envKey); path != "" {
		if err := handle(path); err != nil {
			log.Printf("%v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func handle(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := unix.Mount("/proc/self/ns/net", path, "none", unix.MS_BIND, ""); err != nil {
		return err
	}
	return nil
}

func CreateNetNS(path string) error {
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = []string{envKey + "=" + path}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWNET,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrap(err, string(out))

	}
	return nil
}
