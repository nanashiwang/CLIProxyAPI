package executor

import (
	"context"
)

type streamDeliveryParentKey struct{}

// WithStreamDeliveryParent keeps the client cancellation boundary separate from
// an execution scope that may retire while its terminal frames are being drained.
func WithStreamDeliveryParent(execution, request context.Context) context.Context {
	if execution == nil {
		execution = context.Background()
	}
	if request == nil {
		request = execution
	}
	return context.WithValue(execution, streamDeliveryParentKey{}, request)
}

// StreamDeliveryContext returns the client cancellation boundary for downstream
// delivery only. Keep using the execution context for metadata and upstream I/O.
func StreamDeliveryContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	parent, _ := ctx.Value(streamDeliveryParentKey{}).(context.Context)
	if parent != nil {
		return parent
	}
	return ctx
}
