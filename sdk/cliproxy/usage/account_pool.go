package usage

import "context"

type accountPoolAttributionKey struct{}
type AccountPoolAttribution struct {
	ClientKeyID string
	PoolID      string
}

func WithAccountPoolAttribution(ctx context.Context, keyID, poolID string) context.Context {
	return context.WithValue(ctx, accountPoolAttributionKey{}, AccountPoolAttribution{ClientKeyID: keyID, PoolID: poolID})
}
func AccountPoolAttributionFromContext(ctx context.Context) AccountPoolAttribution {
	if ctx == nil {
		return AccountPoolAttribution{}
	}
	value, _ := ctx.Value(accountPoolAttributionKey{}).(AccountPoolAttribution)
	return value
}
