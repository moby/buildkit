package epoch

import "testing"

func TestParseBuildArgs(t *testing.T) {
	t.Parallel()

	if v, ok := ParseBuildArgs(map[string]string{frontendSourceDateEpochArg: "1700000601"}); !ok || v != "1700000601" {
		t.Fatalf("expected numeric SOURCE_DATE_EPOCH to be forwarded, got %q %v", v, ok)
	}

	if _, ok := ParseBuildArgs(map[string]string{frontendSourceDateEpochArg: "context"}); ok {
		t.Fatal("expected SOURCE_DATE_EPOCH=context to stay frontend-only")
	}

	if v, ok := ParseBuildArgs(map[string]string{frontendSourceDateEpochArg: ""}); !ok || v != "" {
		t.Fatalf("expected empty SOURCE_DATE_EPOCH to remain a valid exporter override, got %q %v", v, ok)
	}
}
