package usage

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRequestMetadataNormalizationAndUnknownSDKContext(t *testing.T) {
	for _, value := range []string{"http", "sse", "ws"} {
		if NormalizeTransport(value) != value {
			t.Fatalf("valid transport %q rejected", value)
		}
	}
	for _, value := range []string{"", "WS", "grpc", "http\n", " ws "} {
		if NormalizeTransport(value) != "" {
			t.Fatalf("invalid transport %q accepted", value)
		}
	}
	if got := NormalizeClientIP("192.0.2.1, 198.51.100.1"); got != "" {
		t.Fatalf("forwarding list accepted: %s", got)
	}
	if got := NormalizeClientIP("[::1]:1234"); got != "" {
		t.Fatalf("unchecked remote address accepted: %s", got)
	}
	if got := NormalizeClientIP(" ::ffff:192.0.2.1 "); got != "192.0.2.1" {
		t.Fatalf("normalized IP=%q", got)
	}
	if got := NormalizeUserAgent("  Client\n/1\x00\u202e "); got != "Client/1" {
		t.Fatalf("UA=%q", got)
	}
	if got := NormalizeUserAgent(strings.Repeat("用", 200)); len(got) > 512 || !utf8.ValidString(got) {
		t.Fatalf("unbounded/invalid UA length=%d", len(got))
	}
	ctx := WithClientTransport(context.Background(), "sse")
	if got := ClientRequestMetadataFromContext(ctx); got != (ClientRequestMetadata{}) {
		t.Fatalf("SDK invented metadata: %+v", got)
	}
}

func TestManagerPublishSnapshotsMetadataBeforeAsyncDispatch(t *testing.T) {
	manager := NewManager(4)
	defer manager.Stop()
	started, release := make(chan struct{}), make(chan struct{})
	records := make(chan Record, 3)
	manager.Register(pluginFuncForManagerTest(func(_ context.Context, record Record) {
		if record.Model == "block-worker" {
			close(started)
			<-release
			return
		}
		records <- record
	}))
	manager.Publish(context.Background(), Record{Model: "block-worker"})
	<-started
	metadata := ClientRequestMetadata{Transport: "sse", ClientIP: "192.0.2.10", UserAgent: "client/1"}
	ctx := WithClientRequestMetadata(context.Background(), metadata)
	manager.Publish(ctx, Record{Model: "captured", UpstreamTransport: "ws"})
	metadata.ClientIP, metadata.UserAgent = "198.51.100.20", "changed/2"
	ctx = WithClientRequestMetadata(ctx, metadata)
	manager.Publish(ctx, Record{Model: "explicit", ClientTransport: "http", ClientIP: "203.0.113.9", UserAgent: "explicit/3", UpstreamTransport: "invalid"})
	manager.Publish(context.Background(), Record{Model: "unknown"})
	close(release)
	for _, want := range []string{"captured", "explicit", "unknown"} {
		select {
		case record := <-records:
			if record.Model != want {
				t.Fatalf("record=%s want=%s", record.Model, want)
			}
			switch want {
			case "captured":
				if record.ClientTransport != "sse" || record.UpstreamTransport != "ws" || record.ClientIP != "192.0.2.10" || record.UserAgent != "client/1" {
					t.Fatalf("snapshot changed: %+v", record)
				}
			case "explicit":
				if record.ClientTransport != "http" || record.UpstreamTransport != "" || record.ClientIP != "203.0.113.9" || record.UserAgent != "explicit/3" {
					t.Fatalf("explicit metadata lost: %+v", record)
				}
			case "unknown":
				if record.ClientTransport != "" || record.UpstreamTransport != "" || record.ClientIP != "" || record.UserAgent != "" {
					t.Fatalf("SDK facts invented: %+v", record)
				}
			}
		case <-time.After(time.Second):
			t.Fatal("usage dispatch stalled")
		}
	}
}
