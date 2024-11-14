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
	require.NotEqual(t, "", res.SchemaURL())

	// No error should have been invoked.
	require.NoError(t, resourceErr)
}
