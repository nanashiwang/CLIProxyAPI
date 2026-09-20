package openai

import (
	"context"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// readResponsesWebsocketInput is the only downstream reader in duplex mode.
// The bounded queue backpressures clients instead of retaining unlimited input.
func readResponsesWebsocketInput(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) <-chan cliproxyexecutor.WebsocketInput {
	input := make(chan cliproxyexecutor.WebsocketInput, 16)
	go func() {
		defer close(input)
		for {
			kind, payload, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				continue
			}
			select {
			case input <- cliproxyexecutor.WebsocketInput{Payload: payload}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return input
}

// Temporary leases end per request and cannot safely retain continuation state.
func (h *OpenAIResponsesAPIHandler) responsesWebsocketSteeringEnabled(ctx context.Context) bool {
	if h == nil || h.Cfg == nil || !h.Cfg.CodexResponseSteering {
		return false
	}
	if h.AuthManager != nil {
		scope, err := h.AuthManager.AccountPoolScope(ctx)
		if err != nil || (scope != nil && scope.LeaseInstance() != "" && sdkaccess.GetGatewayIdentity(ctx).User == "1") {
			return false
		}
	}
	return true
}
