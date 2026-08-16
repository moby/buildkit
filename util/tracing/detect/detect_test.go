package detect

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectExporterIgnoreErrors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		ignoreErrors string
		wantErr      bool
	}{
		{
			name:    "error by default",
			wantErr: true,
		},
		{
			name:         "ignore errors",
			ignoreErrors: "true",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_EXPORTER", "invalid")
			t.Setenv("OTEL_METRICS_EXPORTER", "invalid")
			t.Setenv("OTEL_IGNORE_ERROR", tc.ignoreErrors)

			spanExp, spanErr := NewSpanExporter(t.Context())
			metricExp, metricErr := NewMetricExporter(t.Context())
			if tc.wantErr {
				require.Error(t, spanErr)
				require.Error(t, metricErr)
			} else {
				require.NoError(t, spanErr)
				require.NoError(t, metricErr)
			}
			require.Nil(t, spanExp)
			require.Nil(t, metricExp)
		})
	}
}
