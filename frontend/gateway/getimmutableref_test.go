package gateway

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/moby/buildkit/solver"
)

// A ref that is registered but resolves to a nil ResultProxy should surface as
// an os.ErrNotExist that names both the requested path and the ref id, so
// gateway clients (ReadFile/ReadDir/StatFile) can match on it and users get an
// actionable message.
func TestGetImmutableRefEmptyRefIsNotExist(t *testing.T) {
	lbf := &llbBridgeForwarder{
		refs: map[string]solver.ResultProxy{"emptyref": nil},
	}

	_, err := lbf.getImmutableRef(context.Background(), "emptyref", "/foo/bar")
	if err == nil {
		t.Fatal("expected an error for an empty ref")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error should match os.ErrNotExist, got: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "/foo/bar") || !strings.Contains(msg, "emptyref") {
		t.Fatalf("error should name the path and ref id, got: %q", msg)
	}
}
