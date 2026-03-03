package detect

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
)

func TestResource(t *testing.T) {
	prevHandler := otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetErrorHandler(prevHandler)
	})

	var resourceErr error
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		resourceErr = err
	}))

	res := Resource()

	// Should not have an empty schema url. Only happens when
	// there is a schema conflict.
	require.NotEmpty(t, res.SchemaURL())

	var found bool
	for iter := res.Iter(); iter.Next(); {
		if iter.Attribute().Key == serviceNameKey {
			found = true
			break
		}
	}
	require.True(t, found, "expected to find service name attribute")

	// No error should have been invoked.
	require.NoError(t, resourceErr)
}
