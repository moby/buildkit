package proxy

import (
	"fmt"
	"os"
	"strings"
)

var System []string

func init() {
	System = append(System, getenv("http_proxy")...)
	System = append(System, getenv("https_proxy")...)
	System = append(System, getenv("no_proxy")...)
}

func getenv(lowerKey string) []string {
	v := os.Getenv(lowerKey)
	if len(v) == 0 {
		v = os.Getenv(strings.ToUpper(lowerKey))
	}

	if len(v) == 0 {
		return nil
	}

	return []string{
		fmt.Sprintf("%s=%s", lowerKey, v),
		fmt.Sprintf("%s=%s", strings.ToUpper(lowerKey), v),
	}
}
