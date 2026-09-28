package executor

import "context"

type executionActivityKey struct{}
type upstreamTransportKey struct{}
type accountSwitchedKey struct{}

// WithExecutionActivity observes execution occupancy, independently of leases.
func WithExecutionActivity(ctx context.Context, observe func(bool)) context.Context {
	return context.WithValue(ctx, executionActivityKey{}, observe)
}

// ObserveExecutionActivity lets persistent duplex producers report idle periods.
func ObserveExecutionActivity(ctx context.Context, active bool) {
	if ctx != nil {
		if observe, ok := ctx.Value(executionActivityKey{}).(func(bool)); ok {
			observe(active)
		}
	}
}

// WithUpstreamTransportObserver records a transport decision before execution.
func WithUpstreamTransportObserver(ctx context.Context, observe func(string)) context.Context {
	return context.WithValue(ctx, upstreamTransportKey{}, observe)
}

func ObserveUpstreamTransport(ctx context.Context, transport string) {
	if ctx != nil {
		if observe, ok := ctx.Value(upstreamTransportKey{}).(func(string)); ok {
			observe(transport)
		}
	}
}

// WithAccountSwitched marks a proven credential change within one logical request.
func WithAccountSwitched(ctx context.Context, switched bool) context.Context {
	return context.WithValue(ctx, accountSwitchedKey{}, switched)
}

func AccountSwitched(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	switched, _ := ctx.Value(accountSwitchedKey{}).(bool)
	return switched
}
