package auth

import (
	"context"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"testing"
	"time"
)

func TestHomeRetirementDoesNotDiscardProducedBootstrap(t *testing.T) {
	request, cancelClient := context.WithCancel(context.Background())
	defer cancelClient()
	execution, retire := context.WithCancel(request)
	execution = core.WithStreamDeliveryParent(execution, request)
	frames := make(chan core.StreamChunk, 2)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		time.Sleep(10 * time.Millisecond)
		frames <- core.StreamChunk{Payload: []byte("already-produced")}
		close(frames)
	}()
	defer func() { <-producerDone }()
	retire()
	if execution.Err() == nil {
		t.Fatal("upstream execution was not cancelled")
	}
	buffered, _, err := readStreamBootstrap(execution, frames)
	if err != nil || len(buffered) != 1 {
		t.Fatalf("retirement discarded produced data: %v", err)
	}
	cancelClient()
	_, _, err = readStreamBootstrap(execution, make(chan core.StreamChunk))
	if err != context.Canceled {
		t.Fatalf("client cancellation ignored: %v", err)
	}
}
