package nvidia

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
)

type PackageInfo struct {
	Package     string `json:"Package"`
	Description string `json:"Description"`
}

func searchAptCache(query string) ([]PackageInfo, error) {
	cmd := exec.Command("apt-cache", "search", query)
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return nil, errors.Wrapf(err, "failed to run apt-cache search")
	}

	var packages []PackageInfo
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) == 2 {
			pkg := PackageInfo{
				Package:     strings.TrimSpace(parts[0]),
				Description: strings.TrimSpace(parts[1]),
			}
			packages = append(packages, pkg)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, errors.Wrapf(err, "failed to read apt-cache search output")
	}

	return packages, nil
}
