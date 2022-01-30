package archutil

import (
	"strings"
	"sync"

	"github.com/containerd/containerd/platforms"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
)

var mu sync.Mutex
var arr []ocispecs.Platform

func SupportedPlatforms(noCache bool) []ocispecs.Platform {
	mu.Lock()
	defer mu.Unlock()
	if !noCache && arr != nil {
		return arr
	}
	def := nativePlatform()
	arr = append([]ocispecs.Platform{}, def)

	if def.OS != "linux" {
		return arr
	}

	if p := "amd64"; def.Architecture != p && amd64Supported() == nil {
		arr = append(arr, linux(p))
	}
	if p := "arm64"; def.Architecture != p && arm64Supported() == nil {
		arr = append(arr, linux(p))
	}
	if p := "riscv64"; def.Architecture != p && riscv64Supported() == nil {
		arr = append(arr, linux(p))
	}
	if p := "ppc64le"; def.Architecture != p && ppc64leSupported() == nil {
		arr = append(arr, linux(p))
	}
	if p := "s390x"; def.Architecture != p && s390xSupported() == nil {
		arr = append(arr, linux(p))
	}
	if p := "386"; def.Architecture != p && i386Supported() == nil {
		arr = append(arr, linux(p))
	}
	if p := "mips64le"; def.Architecture != p && mips64leSupported() == nil {
		arr = append(arr, linux(p))
	}
	if p := "mips64"; def.Architecture != p && mips64Supported() == nil {
		arr = append(arr, linux(p))
	}
	if p := "arm"; def.Architecture != p && armSupported() == nil {
		p := linux("arm")
		p.Variant = "v6"
		arr = append(arr, linux("arm"), p)
	} else if def.Architecture == "arm" && def.Variant == "" {
		p := linux("arm")
		p.Variant = "v6"
		arr = append(arr, p)
	}
	return arr
}

//WarnIfUnsupported validates the platforms and show warning message if there is,
//the end user could fix the issue based on those warning, and thus no need to drop
//the platform from the candidates.
func WarnIfUnsupported(pfs []ocispecs.Platform) {
	def := nativePlatform()
	for _, p := range pfs {
		if p.Architecture != def.Architecture {
			if p.Architecture == "amd64" {
				if err := amd64Supported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
			if p.Architecture == "arm64" {
				if err := arm64Supported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
			if p.Architecture == "riscv64" {
				if err := riscv64Supported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
			if p.Architecture == "ppc64le" {
				if err := ppc64leSupported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
			if p.Architecture == "s390x" {
				if err := s390xSupported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
			if p.Architecture == "386" {
				if err := i386Supported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
			if p.Architecture == "mips64le" {
				if err := mips64leSupported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
			if p.Architecture == "mips64" {
				if err := mips64Supported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
			if p.Architecture == "arm" {
				if err := armSupported(); err != nil {
					printPlatformWarning(p, err)
				}
			}
		}
	}
}

func nativePlatform() ocispecs.Platform {
	return platforms.Normalize(platforms.DefaultSpec())
}

func linux(arch string) ocispecs.Platform {
	return ocispecs.Platform{
		OS:           "linux",
		Architecture: arch,
	}
}

func printPlatformWarning(p ocispecs.Platform, err error) {
	if strings.Contains(err.Error(), "exec format error") {
		logrus.Warnf("platform %s cannot pass the validation, kernel support for miscellaneous binary may have not enabled.", platforms.Format(p))
	} else if strings.Contains(err.Error(), "no such file or directory") {
		logrus.Warnf("platforms %s cannot pass the validation, '-F' flag might have not set for 'archutil'.", platforms.Format(p))
	} else {
		logrus.Warnf("platforms %s cannot pass the validation: %s", platforms.Format(p), err.Error())
	}
}
