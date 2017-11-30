package integration

import (
	"os/exec"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Sandbox interface {
	Address() string
	PrintLogs(*testing.T)
	Cmd(...string) *exec.Cmd
}

type Worker interface {
	New() (Sandbox, func() error, error)
	Name() string
}

type Test func(*testing.T, Sandbox)

var defaultWorkers []Worker

func register(w Worker) {
	defaultWorkers = append(defaultWorkers, w)
}

func List() []Worker {
	return defaultWorkers
}

func Run(t *testing.T, testCases map[string]Test) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	for _, br := range List() {
		for name, tc := range testCases {
			ok := t.Run(name+"/worker="+br.Name(), func(t *testing.T) {
				sb, close, err := br.New()
				if err != nil {
					if errors.Cause(err) == ErrorRequirements {
						t.Skip(err.Error())
					}
					require.NoError(t, err)
				}
				defer func() {
					assert.NoError(t, close())
					if t.Failed() {
						sb.PrintLogs(t)
					}
				}()
				tc(t, sb)
			})
			require.True(t, ok)
		}
	}
}
