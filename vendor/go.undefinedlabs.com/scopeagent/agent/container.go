package agent

import (
	"io/ioutil"
	"regexp"
	"runtime"
	"sync"
)

var (
	runningInContainerOnce sync.Once
	runningInContainer     bool
	containerRegex         = regexp.MustCompile(`(?m)\/docker\/|\/ecs\/|\/docker-|\/kubepods\/|\/actions_job\/|\/lxc\/`)
)

// gets if the current process is running inside a container
func isRunningInContainer() bool {
	runningInContainerOnce.Do(func() {
		if runtime.GOOS != "linux" {
			runningInContainer = false
			return
		}
		content, err := ioutil.ReadFile("/proc/1/cgroup")
		if err != nil {
			runningInContainer = false
			return
		}
		runningInContainer = containerRegex.Match(content)
	})
	return runningInContainer
}
