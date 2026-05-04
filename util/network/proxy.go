package network

import "context"

type proxyPolicyKey struct{}

// ProxyPolicy authorizes requests made through a BuildKit-owned exec proxy.
type ProxyPolicy interface {
	CheckProxyRequest(context.Context, string) error
}

// WithProxyPolicy attaches a proxy request authorizer to ctx.
func WithProxyPolicy(ctx context.Context, p ProxyPolicy) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, proxyPolicyKey{}, p)
}

// ProxyPolicyFromContext returns the proxy request authorizer attached to ctx.
func ProxyPolicyFromContext(ctx context.Context) ProxyPolicy {
	p, _ := ctx.Value(proxyPolicyKey{}).(ProxyPolicy)
	return p
}

// ProxyNamespace is implemented by network namespaces that expose an internal
// HTTP(S) proxy to the container.
type ProxyNamespace interface {
	ProxyEnv() []string
	ProxyCACert() []byte
}
