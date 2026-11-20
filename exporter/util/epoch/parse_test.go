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

func TestParseMetadata(t *testing.T) {
	tm, err := ParseMetadata(nil)
	if err != nil || tm != nil {
		t.Fatalf("expected no epoch, got %v, %v", tm, err)
	}
	tm, err = ParseMetadata(map[string][]byte{"source.date.epoch": []byte("42"), "source.date.epoch/linux/amd64": []byte("1")})
	if err != nil || tm == nil || tm.Unix() != 42 {
		t.Fatalf("expected global epoch, got %v, %v", tm, err)
	}
	tm, err = ParseMetadata(map[string][]byte{"source.date.epoch/linux/amd64": []byte("50"), "source.date.epoch/linux/arm64": []byte("43")})
	if err != nil || tm == nil || tm.Unix() != 43 {
		t.Fatalf("expected earliest per-platform epoch, got %v, %v", tm, err)
	}
	if _, err := ParseMetadata(map[string][]byte{"source.date.epoch": []byte("nope")}); err == nil {
		t.Fatal("expected error")
	}
}
