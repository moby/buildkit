package testutil

import (
	"context"
	"testing"

	"go.undefinedlabs.com/scopeagent"
	"go.undefinedlabs.com/scopeagent/env"
	scopetesting "go.undefinedlabs.com/scopeagent/instrumentation/testing"
)

func GetContext(t *testing.T) context.Context {
	if env.ScopeDsn.Value == "" {
		return context.TODO()
	}

	return scopeagent.GetContextFromTest(t)
}

// This method needs to be called inside of the subtest code.
func SetTestCode(t *testing.T) {
	if env.ScopeDsn.Value == "" {
		return
	}

	scopeagent.SetTestCodeFromCallerSkip(t, 1)
}

func GetTracedTest(t *testing.T) *scopetesting.Test {
	return scopeagent.GetTest(t)
}
