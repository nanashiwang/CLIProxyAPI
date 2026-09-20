package auth

import (
	"reflect"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAvailabilityViewBoundaries(t *testing.T) {
	now := time.Now().UTC()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	for _, tc := range []struct {
		name    string
		auth    Auth
		blocked bool
		status  Status
		retry   time.Time
	}{
		{"deadline boundary", Auth{Status: StatusError, Unavailable: true, NextRetryAfter: now}, false, StatusActive, time.Time{}},
		{"future cooldown", Auth{Status: StatusError, Unavailable: true, NextRetryAfter: future}, true, StatusError, future},
		{"later quota deadline", Auth{Status: StatusError, Unavailable: true, NextRetryAfter: past, Quota: QuotaState{Exceeded: true, NextRecoverAt: future}}, true, StatusError, future},
		{"quota without reset", Auth{Status: StatusError, Quota: QuotaState{Exceeded: true}}, true, StatusError, time.Time{}},
		{"quota with healthy model", Auth{Status: StatusError, Quota: QuotaState{Exceeded: true}, ModelStates: map[string]*ModelState{"healthy": {Status: StatusActive}}}, true, StatusError, time.Time{}},
		{"confirmed exhaustion", Auth{Status: StatusError, Unavailable: true, NextRetryAfter: past, CodexQuota: &CodexQuotaSnapshot{Exhausted: true}}, true, StatusError, time.Time{}},
		{"internal quota fence", Auth{Status: StatusError, Unavailable: true, NextRetryAfter: past, codexQuotaBlocked: true}, true, StatusError, time.Time{}},
		{"persistent failure", Auth{Status: StatusError, Unavailable: true, StatusMessage: "persistent failure"}, true, StatusError, time.Time{}},
		{"401 with pending refresh", Auth{Status: StatusError, Unavailable: true, NextRetryAfter: past, NextRefreshAfter: future, LastError: &Error{HTTPStatus: 401}}, true, StatusError, time.Time{}},
		{"disabled", Auth{Disabled: true, Status: StatusDisabled, Unavailable: true, NextRetryAfter: past}, true, StatusDisabled, past},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.auth.Clone()
			unavailable, status, _, retry := tc.auth.AvailabilityView(now)
			if unavailable != tc.blocked || status != tc.status || !retry.Equal(tc.retry) {
				t.Fatalf("got %v %v %v", unavailable, status, retry)
			}
			if !reflect.DeepEqual(before, tc.auth.Clone()) {
				t.Fatal("view changed routing snapshot")
			}
		})
	}
}

func TestAvailabilityViewExpiredCooldownPreservesExclusiveLease(t *testing.T) {
	m, _, _ := leaseManager(t)
	req := coreexecutor.Request{Model: "pool-model"}
	opts := coreexecutor.Options{}
	ctx, done, err := m.beginPoolLease(leaseCaller("key-all", "101"), []string{"pool-test"}, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	binding := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease)
	m.mu.Lock()
	a := m.auths[binding.Lease.Credential]
	a.Status = StatusError
	a.Unavailable = true
	a.NextRetryAfter = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	before, err := m.AccountPoolLeases()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		a, _ := m.GetByID(binding.Lease.Credential)
		blocked, status, _, _ := a.AvailabilityView(time.Now())
		if blocked || status != StatusActive {
			t.Fatal("expired transient cooldown not projected active")
		}
	}
	after, err := m.AccountPoolLeases()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("view altered lease occupancy")
	}
	a, _ = m.GetByID(binding.Lease.Credential)
	if !a.Unavailable || a.Status != StatusError {
		t.Fatal("view mutated stored auth")
	}
	for _, user := range []string{"2", "3"} {
		_, release, err := m.beginPoolLease(leaseCaller("key-all", user), []string{"pool-test"}, req, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	if _, release, err := m.beginPoolLease(leaseCaller("key-all", "4"), []string{"pool-test"}, req, opts); err == nil {
		release()
		t.Fatal("view freed an occupied exclusive account")
	}
	// Existing owner remains pinned after presentation recovery.
	again, release, err := m.beginPoolLease(leaseCaller("key-all", "101"), []string{"pool-test"}, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if again.Value(requestPoolLeaseKey{}).(*requestPoolLease).Lease.ID != binding.Lease.ID {
		t.Fatal("view replaced owner's lease")
	}
}
