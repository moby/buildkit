package detect

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk"
	"go.opentelemetry.io/otel/sdk/resource"
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

	// Keep our schema independent of the version used by the OTel SDK.
	require.Equal(t, schemaURL, res.SchemaURL())

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

func TestResourceWithFromEnv(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "buildkit-test")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=overridden,custom.attribute=value")

	res, err := resource.New(t.Context(),
		resource.WithDetectors(serviceNameDetector{}),
		resource.WithFromEnv(),
		resource.WithDetectors(telemetrySDK{}),
	)
	require.NoError(t, err)
	require.Equal(t, schemaURL, res.SchemaURL())
	require.ElementsMatch(t, []attribute.KeyValue{
		attribute.String(serviceNameKey, "buildkit-test"),
		attribute.String("custom.attribute", "value"),
		attribute.String(telemetrySDKNameKey, "opentelemetry"),
		attribute.String(telemetrySDKLanguageKey, "go"),
		attribute.String(telemetrySDKVersionKey, sdk.Version()),
	}, res.Attributes())
}
