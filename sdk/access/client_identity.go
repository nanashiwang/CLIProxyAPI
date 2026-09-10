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
	if id := ClientKeyIdentity(src); id != "" {
		return context.WithValue(dst, clientKeyIdentityContextKey{}, id)
	}
	return dst
}
