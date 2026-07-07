package winlayers

import (
	"context"
	"runtime"
)

type contextKeyT string

var contextKey = contextKeyT("buildkit/winlayers-layeros")

// UseWindowsLayerMode marks the context as handling a Windows layer.
// Kept for backward compatibility; equivalent to SetLayerOS(ctx, "windows").
func UseWindowsLayerMode(ctx context.Context) context.Context {
	return SetLayerOS(ctx, "windows")
}

// SetLayerOS records the OS of the layer being processed in the context, used
// to decide whether cross-platform transformation is needed.
func SetLayerOS(ctx context.Context, os string) context.Context {
	return context.WithValue(ctx, contextKey, os)
}

// GetLayerOS returns the OS of the layer set in the context, or empty string if not set.
func GetLayerOS(ctx context.Context) string {
	v, _ := ctx.Value(contextKey).(string)
	return v
}

// returns true if the context is set to use Windows layer mode on a non-Windows host.
func hasWindowsLayerMode(ctx context.Context) bool {
	return GetLayerOS(ctx) == "windows" && runtime.GOOS != "windows"
}

// returns true if the context is set to use Linux layer mode on a Windows host.
func hasLinuxLayerMode(ctx context.Context) bool {
	return GetLayerOS(ctx) == "linux" && runtime.GOOS == "windows"
}

// needsTransformation returns true if the layer OS differs from the host OS,
// meaning cross-platform layer transformation is required.
func needsTransformation(ctx context.Context) bool {
	return hasWindowsLayerMode(ctx) || hasLinuxLayerMode(ctx)
}
