package local

import "github.com/Microsoft/go-winio"

func runWithPrivileges(fn func() error) error {
	return winio.RunWithPrivileges([]string{winio.SeBackupPrivilege}, fn)
}
