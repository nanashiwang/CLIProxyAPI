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
	for _, c := range []context.Context{leaseCaller("key-all", "101"), leaseCaller("key-multi", "101"), leaseCaller("key-all", "2")} {
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
	ctx := leaseCaller("key-all", "101")
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
	ctx, done, err := m.beginPoolLease(leaseCaller("key-all", "101"), []string{"pool-test"}, coreexecutor.Request{Model: "pool-model"}, coreexecutor.Options{})
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
	_, err := m.Execute(leaseCaller("key-all", "101"), []string{"pool-test"}, coreexecutor.Request{Model: "pool-model", Payload: []byte(`{"previous_response_id":"old"}`)}, coreexecutor.Options{})
	var poolErr *Error
	if !errors.As(err, &poolErr) || poolErr.Code != "pool_lease_session_expired" {
		t.Fatal(err)
	}
}

func TestLeaseStreamFailoverAfterTargetedCooldownCannotEscape(t *testing.T) {
	m, c, e := leaseManager(t)
	ctx := leaseCaller("key-all", "101")
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

func TestLeaseTerminalFailureReplacesAccountInEveryMode(t *testing.T) {
	for _, mode := range []string{"normal", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			m, _, e := leaseManager(t)
			ctx := leaseCaller("key-all", "101")
			req := coreexecutor.Request{Model: "pool-model"}
			if _, err := m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			before, _ := m.AccountPoolLeases()
			e.errors = map[string]error{e.ids[0]: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}}
			var err error
			switch mode {
			case "normal":
				_, err = m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
			case "count":
				_, err = m.ExecuteCount(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
			case "stream":
				var r *coreexecutor.StreamResult
				r, err = m.ExecuteStream(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
				if err == nil {
					for c := range r.Chunks {
						if c.Err != nil {
							t.Fatal(c.Err)
						}
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			after, _ := m.AccountPoolLeases()
			if len(after) != 1 || after[0].Credential == before[0].Credential || after[0].Group != before[0].Group || after[0].ID == before[0].ID || !after[0].Expires.Equal(before[0].Expires) {
				t.Fatalf("invalid replacement: %+v -> %+v", before, after)
			}
			_, err = m.Execute(ctx, []string{"pool-test"}, coreexecutor.Request{Model: "pool-model", Payload: []byte(`{"previous_response_id":"old"}`)}, coreexecutor.Options{})
			var pe *Error
			if !errors.As(err, &pe) || pe.Code != "pool_lease_session_expired" {
				t.Fatalf("unsafe continuation: %v", err)
			}
		})
	}
}

func TestLeaseGenericRateLimitKeepsAccount(t *testing.T) {
	m, _, e := leaseManager(t)
	ctx := leaseCaller("key-all", "101")
	req := coreexecutor.Request{Model: "pool-model"}
	m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
	before, _ := m.AccountPoolLeases()
	e.errors = map[string]error{e.ids[0]: &Error{HTTPStatus: 429, Message: "rate_limit_exceeded"}}
	if _, err := m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{}); err == nil {
		t.Fatal("expected rate limit")
	}
	after, _ := m.AccountPoolLeases()
	if before[0].ID != after[0].ID {
		t.Fatal("transient rate limit replaced account")
	}
}

func TestLeaseReplacementWaitsForOtherRequestAndResumesOnAdmission(t *testing.T) {
	m, _, e := leaseManager(t)
	ctx := leaseCaller("key-all", "101")
	req := coreexecutor.Request{Model: "pool-model"}
	held, done, err := m.beginPoolLease(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	old := held.Value(requestPoolLeaseKey{}).(*requestPoolLease).Lease
	e.errors = map[string]error{old.Credential: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}}
	_, err = m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "pool_lease_failover_pending" {
		t.Fatal(err)
	}
	done()
	if _, err = m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	after, _ := m.AccountPoolLeases()
	if after[0].Credential == old.Credential {
		t.Fatal("failed account was retained")
	}
}

func TestLeaseTerminalFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
		want   bool
	}{
		{429, "usage_limit_reached", true}, {429, "You've hit your usage limit", true}, {429, "rate_limit_exceeded", false}, {401, "unauthorized", true}, {403, "account_deactivated", true}, {403, "content policy", false}, {503, "usage_limit_reached", false},
	} {
		if got := poolLeaseTerminalFailure(&Error{HTTPStatus: tc.status, Message: tc.msg}); got != tc.want {
			t.Fatalf("%+v = %v", tc, got)
		}
	}
}

func TestLeaseDiscardedProducerBlocksReplacement(t *testing.T) {
	m, _, _ := leaseManager(t)
	ctx, done, err := m.beginPoolLease(leaseCaller("key-all", "101"), []string{"pool-test"}, coreexecutor.Request{Model: "pool-model"}, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	old := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease).Lease
	ch := make(chan coreexecutor.StreamChunk)
	m.discardLeasedStream(ctx, ch)
	store := m.poolLeaseRuntime.store
	l, _, _ := store.Lookup(old.Owner, old.Policy, time.Now())
	if l.Active != 2 {
		t.Fatal(l)
	}
	if _, _, err = store.Replace(old, nil, time.Now()); err == nil {
		t.Fatal("replaced with active producer")
	}
	close(ch)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		l, _, _ = store.Lookup(old.Owner, old.Policy, time.Now())
		if l.Active == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("producer lease retention leaked")
}

type leaseCommittedErrorExecutor struct{ *poolCaptureExecutor }

func (e *leaseCommittedErrorExecutor) ExecuteStream(ctx context.Context, a *Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if _, err := e.Execute(ctx, a, req, opts); err != nil {
		return nil, err
	}
	ch := make(chan coreexecutor.StreamChunk, 2)
	ch <- coreexecutor.StreamChunk{Payload: []byte("already emitted")}
	ch <- coreexecutor.StreamChunk{Err: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}
func TestLeaseCommittedStreamIsNotReplayed(t *testing.T) {
	m, _, e := leaseManager(t)
	m.RegisterExecutor(&leaseCommittedErrorExecutor{e})
	req := coreexecutor.Request{Model: "pool-model"}
	ctx := leaseCaller("key-all", "101")
	r, err := m.ExecuteStream(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	chunks := 0
	for c := range r.Chunks {
		chunks++
		if chunks == 1 && c.Err != nil {
			t.Fatal(c.Err)
		}
	}
	if chunks != 2 || len(e.ids) != 1 {
		t.Fatalf("stream replayed: chunks=%d ids=%v", chunks, e.ids)
	}
	before, _ := m.AccountPoolLeases()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ls, _ := m.AccountPoolLeases()
		if ls[0].Active == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	m.RegisterExecutor(e)
	if _, err = m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	after, _ := m.AccountPoolLeases()
	if before[0].Credential == after[0].Credential {
		t.Fatal("next request did not replace failed account")
	}
}

func TestUserOneUsesTemporaryAccountsInEveryMode(t *testing.T) {
	for _, mode := range []string{"normal", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _ := leaseManager(t)
			ctx := leaseCaller("key-all", "1")
			req := coreexecutor.Request{Model: "pool-model"}
			var err error
			switch mode {
			case "normal":
				_, err = m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
			case "count":
				_, err = m.ExecuteCount(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
			case "stream":
				var r *coreexecutor.StreamResult
				r, err = m.ExecuteStream(ctx, []string{"pool-test"}, req, coreexecutor.Options{})
				if err == nil {
					for c := range r.Chunks {
						if c.Err != nil {
							t.Fatal(c.Err)
						}
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				ls, _ := m.AccountPoolLeases()
				if len(ls) == 0 {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("user 1 left hour lease")
		})
	}
}

func TestTemporaryIdentityAndConcurrentIsolation(t *testing.T) {
	m, _, _ := leaseManager(t)
	req := coreexecutor.Request{Model: "pool-model"}
	providers := []string{"pool-test"}
	normal, done, err := m.beginPoolLease(leaseCaller("key-all", "2"), providers, req, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	first, done1, err := m.beginPoolLease(leaseCaller("key-all", "1"), providers, req, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer done1()
	second, done2, err := m.beginPoolLease(leaseCaller("key-all", "1"), providers, req, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer done2()
	seen := map[string]bool{}
	for _, ctx := range []context.Context{normal, first, second} {
		l := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease).Lease
		if seen[l.Credential] {
			t.Fatal("concurrent accounts shared")
		}
		seen[l.Credential] = true
		scope, err := m.AccountPoolScope(ctx)
		if err != nil || !scope.Allows(l.Credential) {
			t.Fatal("wrong request binding", err)
		}
	}
	wrong := sdkaccess.WithGatewayIdentity(poolCaller("key-all"), "wrong-instance", "1")
	if _, _, err = m.beginPoolLease(wrong, providers, req, coreexecutor.Options{}); err == nil {
		t.Fatal("wrong instance bypassed identity validation")
	}
	done1()
	done2()
	rows, _ := m.AccountPoolLeases()
	if len(rows) != 1 || rows[0].Temporary {
		t.Fatal(rows)
	}
}

func TestTemporaryStreamCancellationKeepsReservationUntilDrain(t *testing.T) {
	m, _, _ := leaseManager(t)
	ctx, done, err := m.beginPoolLease(leaseCaller("key-all", "1"), []string{"pool-test"}, coreexecutor.Request{Model: "pool-model"}, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw := make(chan coreexecutor.StreamChunk)
	ctx, cancel := context.WithCancel(ctx)
	result := holdPoolLeaseStream(ctx, &coreexecutor.StreamResult{Chunks: raw}, done)
	cancel()
	ls, _ := m.AccountPoolLeases()
	if len(ls) != 1 || !ls[0].Temporary {
		t.Fatal("released before upstream ended")
	}
	close(raw)
	for range result.Chunks {
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ls, _ = m.AccountPoolLeases()
		if len(ls) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("temporary stream leaked")
}

func TestTemporaryContinuationDoesNotAllocate(t *testing.T) {
	m, _, _ := leaseManager(t)
	_, err := m.Execute(leaseCaller("key-all", "1"), []string{"pool-test"}, coreexecutor.Request{Model: "pool-model", Payload: []byte(`{"previous_response_id":"old"}`)}, coreexecutor.Options{})
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "pool_temporary_session_unsupported" {
		t.Fatal(err)
	}
	ls, _ := m.AccountPoolLeases()
	if len(ls) != 0 {
		t.Fatal("continuation allocated account")
	}
}

func TestTemporaryQuotaFailoverDoesNotLeaveHourLease(t *testing.T) {
	m, c, e := leaseManager(t)
	first := c.AccountPools.Groups[1].CredentialIDs[0]
	e.errors = map[string]error{first: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}}
	if _, err := m.Execute(leaseCaller("key-all", "1"), []string{"pool-test"}, coreexecutor.Request{Model: "pool-model"}, coreexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(e.ids) != 2 || e.ids[0] == e.ids[1] {
		t.Fatal("temporary failover did not occur", e.ids)
	}
	rows, _ := m.AccountPoolLeases()
	if len(rows) != 0 {
		t.Fatal("temporary failover retained lease", rows)
	}
}
