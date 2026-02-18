package executor

import "testing"

func TestValidContainerID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "lowercase", id: "abc123", wantErr: false},
		{name: "uppercase", id: "AbC123", wantErr: false},
		{name: "empty", id: "", wantErr: true},
		{name: "dash", id: "abc-123", wantErr: true},
		{name: "slash", id: "abc/123", wantErr: true},
		{name: "underscore", id: "abc_123", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidContainerID(tc.id)
			if tc.wantErr && err == nil {
				t.Fatalf("expected an error for id %q", tc.id)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error for id %q, got %v", tc.id, err)
			}
		})
	}
}
