package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestResultPolicyPreservesAccountLeaseAndTargetedCooldown(t *testing.T) {
	for _, drop := range []bool{false, true} {
		name := "demote"
		if drop {
			name = "drop"
		}
		t.Run(name, func(t *testing.T) {
			m, cfg, e := leaseManager(t)
			ctx := leaseCaller("key-all", "owner")
			req := coreexecutor.Request{Model: "pool-model"}
			if _, err := m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			id := e.ids[0]
			leases, err := m.AccountPoolLeases()
			if err != nil || len(leases) != 1 {
				t.Fatal(leases, err)
			}
			original := leases[0]
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(id, "pool-test", []*registry.ModelInfo{{ID: "pool-model"}, {ID: "sibling-model"}})
			m.RefreshSchedulerEntry(id)
			m.MarkResult(ctx, Result{AuthID: id, Model: "sibling-model", Success: true})
			store := &recordingCooldownStore{}
			m.SetCooldownStateStore(store)
			hook := &recordingHook{}
			m.hook = hook
			m.SetResultPolicy(ResultPolicyFunc(func(ctx context.Context, r Result) Result {
				if err := m.CheckAccountPoolAccess(ctx, r.AuthID); err != nil {
					t.Fatal("policy lost caller context", err)
				}
				if drop {
					r.AuthID = ""
				} else {
					r.CredentialScope = false
				}
				return r
			}))
			retry := time.Hour
			result := Result{AuthID: id, Provider: "pool-test", Model: "pool-model", CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429}}
			m.MarkResult(ctx, result)
			before, _ := m.GetByID(id)
			if drop {
				if before.Unavailable || len(store.getRecords()) != 0 || hook.lastResult.Load() != nil {
					t.Fatal("dropped result mutated cooldown or hooks")
				}
			} else {
				if before.Quota.Reason == "credential_quota" {
					t.Fatal("policy did not demote credential cooldown")
				}
				// A subsequent shorter policy-adjusted failure cannot shorten a live deadline.
				short := time.Minute
				result.RetryAfter = &short
				m.MarkResult(ctx, result)
				after, _ := m.GetByID(id)
				if !after.ModelStates["pool-model"].NextRetryAfter.Equal(before.ModelStates["pool-model"].NextRetryAfter) {
					t.Fatal("policy shortened active cooldown")
				}
				for _, record := range store.getRecords() {
					if record.Model == "" {
						t.Fatal("persisted a credential cooldown after demotion")
					}
				}
				if _, err := m.Execute(ctx, []string{"pool-test"}, req, coreexecutor.Options{}); err == nil {
					t.Fatal("cooling owner escaped its leased account")
				}
				if len(e.ids) != 1 {
					t.Fatal("cooling request reached another account", e.ids)
				}
			}
			// A sibling model can use the same lease, but never a different account.
			if _, err := m.Execute(ctx, []string{"pool-test"}, coreexecutor.Request{Model: "sibling-model"}, coreexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			if e.ids[len(e.ids)-1] != id {
				t.Fatal("sibling model changed leased account")
			}
			if _, err := m.Execute(leaseCaller("key-all", "second"), []string{"pool-test"}, req, coreexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			secondID := e.ids[len(e.ids)-1]
			policy := cfg.CompileAccountPoolPolicy()
			if secondID == id || policy.GroupForCredential(secondID) != policy.GroupForCredential(id) {
				t.Fatal("another owner could not use a free account in the same group")
			}
			leases, err = m.AccountPoolLeases()
			if err != nil {
				t.Fatal(err)
			}
			for _, lease := range leases {
				if lease.ID == original.ID {
					if lease.Credential != original.Credential || !lease.Expires.Equal(original.Expires) || lease.Active != 0 {
						t.Fatal("result handling changed or leaked lease", lease)
					}
					return
				}
			}
			t.Fatal("original lease disappeared")
		})
	}
}
