package auth

import (
	"context"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"testing"
	"time"
)

func leaseManager(t *testing.T) (*Manager, *config.Config, *poolCaptureExecutor) {
	m, c, e, _ := newPoolManager(t)
	c.AuthDir = t.TempDir()
	c.AccountPools.Groups[1].Lease = true
	c.AccountPools.Groups[2].Lease = true
	for i := range c.AccountPools.KeyRules {
		if c.AccountPools.KeyRules[i].Scope == "all" || c.AccountPools.KeyRules[i].KeyHash == config.ClientKeyFingerprint("key-multi") {
			c.AccountPools.KeyRules[i].LeaseInstance = "newapi-main"
		}
	}
	m.SetConfig(c)
	t.Cleanup(func() {
		if s := m.poolLeaseRuntime.store; s != nil {
			s.Close()
		}
	})
	return m, c, e
}
func leaseCaller(key, user string) context.Context {
	return sdkaccess.WithGatewayIdentity(poolCaller(key), "newapi-main", user)
}
func TestLeaseIdentityAndPoolIsolationAcrossKeys(t *testing.T) {
	m, _, e := leaseManager(t)
	req := coreexecutor.Request{Model: "pool-model"}
	opts := coreexecutor.Options{}
	if _, err := m.Execute(poolCaller("key-all"), []string{"pool-test"}, req, opts); err == nil {
		t.Fatal("accepted missing identity")
	}
	for _, c := range []context.Context{leaseCaller("key-all", "1"), leaseCaller("key-multi", "1"), leaseCaller("key-all", "2")} {
		if _, err := m.Execute(c, []string{"pool-test"}, req, opts); err != nil {
			t.Fatal(err)
		}
	}
	policy := m.runtimeConfigSnapshot().AccountPoolPolicy
	if e.ids[0] != e.ids[1] || e.ids[0] == e.ids[2] || policy.GroupForCredential(e.ids[0]) != policy.GroupForCredential(e.ids[2]) {
		t.Fatal("lease isolation failed", e.ids)
	}
	if _, err := m.Execute(leaseCaller("key-all", "3"), []string{"pool-test"}, req, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Execute(leaseCaller("key-all", "4"), []string{"pool-test"}, req, opts); err == nil {
		t.Fatal("fourth user stole occupied account")
	}
	if _, err := m.Execute(poolCaller("key-a"), []string{"pool-test"}, req, opts); err == nil {
		t.Fatal("static key bypassed lease")
	}
}
func TestLeaseRetryCannotEscapeAndConfigChangeBlocks(t *testing.T) {
	m, c, e := leaseManager(t)
	req := coreexecutor.Request{Model: "pool-model"}
	ctx := leaseCaller("key-all", "1")
	if _, err := m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	leasedID := e.ids[0]
	e.failures[leasedID] = true
	if _, err := m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{}); err == nil {
		t.Fatal("failure escaped pool")
	}
	for _, id := range e.ids {
		if id != leasedID {
			t.Fatal("used other pool")
		}
	}
	c.AccountPools.Groups[1].CredentialIDs = nil
	m.SetConfig(c)
	if _, err := m.Execute(leaseCaller("key-all", "2"), []string{"pool-test"}, req, coreexecutor.Options{}); err == nil {
		t.Fatal("membership change bypassed active lease")
	}
}
func TestLeaseStreamRetainsInFlightUntilProducerCloses(t *testing.T) {
	m, _, _ := leaseManager(t)
	ctx, done, err := m.beginPoolLease(leaseCaller("key-all", "1"), []string{"pool-test"}, coreexecutor.Request{Model: "pool-model"}, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	binding := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease)
	chunks := make(chan coreexecutor.StreamChunk)
	ctx, cancel := context.WithCancel(ctx)
	result := holdPoolLeaseStream(ctx, &coreexecutor.StreamResult{Chunks: chunks}, done)
	cancel()
	store := m.poolLeaseRuntime.store
	lease, ok, err := store.Lookup(binding.Owner, binding.Lease.Policy, time.Now())
	if err != nil || !ok || lease.Active != 1 {
		t.Fatal(lease, ok, err)
	}
	close(chunks)
	for range result.Chunks {
	}
	// Channel close happens before the deferred in-flight release.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		lease, _, _ = store.Lookup(binding.Owner, binding.Lease.Policy, time.Now())
		if lease.Active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("in-flight lease leaked")
}
func TestExpiredContinuationDoesNotAllocateAnotherPool(t *testing.T) {
	m, _, _ := leaseManager(t)
	_, err := m.Execute(leaseCaller("key-all", "1"), []string{"pool-test"}, coreexecutor.Request{Model: "pool-model", Payload: []byte(`{"previous_response_id":"old"}`)}, coreexecutor.Options{})
	var poolErr *Error
	if !errors.As(err, &poolErr) || poolErr.Code != "pool_lease_session_expired" {
		t.Fatal(err)
	}
}

func TestLeaseStreamFailoverAfterTargetedCooldownCannotEscape(t *testing.T) {
	m, c, e := leaseManager(t)
	ctx := leaseCaller("key-all", "1")
	req := coreexecutor.Request{Model: "pool-model"}
	result, err := m.ExecuteStream(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	group := m.runtimeConfigSnapshot().AccountPoolPolicy.GroupForCredential(e.ids[0])
	for _, candidateGroup := range c.AccountPools.Groups {
		if candidateGroup.ID == group {
			for _, id := range candidateGroup.CredentialIDs {
				e.failures[id] = true
			}
		}
	}
	result, err = m.ExecuteStream(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
	if err == nil {
		for range result.Chunks {
		}
		t.Fatal("stream retry escaped the exhausted lease")
	}
	for _, id := range e.ids {
		if m.runtimeConfigSnapshot().AccountPoolPolicy.GroupForCredential(id) != group {
			t.Fatalf("retry escaped lease: %s", id)
		}
	}
}
