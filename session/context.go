package session

import "context"

type contextKeyT string

var contextKey = contextKeyT("buildkit/session-uuid")

func NewContext(ctx context.Context, uuid string) context.Context {
	if uuid != "" {
		return context.WithValue(ctx, contextKey, uuid)
	}
	return ctx
}

func FromContext(ctx context.Context) string {
	v := ctx.Value(contextKey)
	if v == nil {
		return ""
	}
	return v.(string)
}
