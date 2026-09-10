package access

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type clientKeyIdentityContextKey struct{}

// WithClientKeyIdentity must only be called after successful client authentication.
func WithClientKeyIdentity(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, clientKeyIdentityContextKey{}, config.ClientKeyFingerprint(principal))
}
func ClientKeyIdentity(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(clientKeyIdentityContextKey{}).(string)
	return id
}
func CopyClientKeyIdentity(dst, src context.Context) context.Context {
	if src == nil {
		return dst
	}
	if id := ClientKeyIdentity(src); id != "" {
		dst = context.WithValue(dst, clientKeyIdentityContextKey{}, id)
	}
	if identity, ok := src.Value(gatewayIdentityKey{}).(GatewayIdentity); ok {
		dst = context.WithValue(dst, gatewayIdentityKey{}, identity)
	}
	return dst
}

type gatewayIdentityKey struct{}
type GatewayIdentity struct {
	Instance string
	User     string
}

// WithGatewayIdentity is called after client authentication; the key policy must
// additionally authorize this instance before the identity is used for a lease.
func WithGatewayIdentity(ctx context.Context, instance, user string) context.Context {
	return context.WithValue(ctx, gatewayIdentityKey{}, GatewayIdentity{instance, user})
}
func GetGatewayIdentity(ctx context.Context) GatewayIdentity {
	if ctx == nil {
		return GatewayIdentity{}
	}
	v, _ := ctx.Value(gatewayIdentityKey{}).(GatewayIdentity)
	return v
}
