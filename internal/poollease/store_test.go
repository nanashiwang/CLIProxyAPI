package poollease

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "leases"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestConcurrentUsersAndFixedExpiry(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	allowed := map[string]bool{"a": true, "b": true}
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, done, e := s.Acquire("alice", "v1", allowed, testCandidates("a", "b"), now)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- l.ID
			done()
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("same user acquired multiple leases")
		}
		id = got
	}
	l, done, e := s.Acquire("alice", "v1", allowed, testCandidates("b"), now.Add(30*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if !l.Expires.Equal(now.Add(time.Hour)) {
		t.Fatal("request renewed the fixed lease")
	}
	done()
	b, done, e := s.Acquire("bob", "v1", allowed, testCandidates("a", "b"), now)
	if e != nil {
		t.Fatal(e)
	}
	if b.Group == l.Group {
		t.Fatal("users shared pool")
	}
	done()
	_, _, e = s.Acquire("charlie", "v1", allowed, testCandidates("a", "b"), now)
	if !errors.Is(e, ErrBusy) {
		t.Fatal(e)
	}
}
func TestExpiredInFlightLeaseCannotBeReassigned(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	allowed := map[string]bool{"a": true}
	_, done, e := s.Acquire("alice", "v1", allowed, testCandidates("a"), now)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s.Acquire("bob", "v1", allowed, testCandidates("a"), now.Add(61*time.Minute))
	if !errors.Is(e, ErrBusy) {
		t.Fatal("reassigned in-flight pool", e)
	}
	done()
	done()
	l, release, e := s.Acquire("bob", "v1", allowed, testCandidates("a"), now.Add(61*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	release()
	if l.Owner != "bob" {
		t.Fatal(l)
	}
}
func TestRecoveryLockAndPolicyProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	l, done, e := s.Acquire("alice", "v1", map[string]bool{"a": true}, testCandidates("a"), now)
	if e != nil {
		t.Fatal(e)
	}
	done()
	if other, e := Open(path); e == nil {
		other.Close()
		t.Fatal("second process opened leased state")
	}
	if !errors.Is(s.CheckPolicy("v2", now), ErrPolicy) {
		t.Fatal("accepted changed membership")
	}
	s.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got, ok, e := s.Lookup("alice", "v1", now)
	if e != nil || !ok || got.ID != l.ID || got.Active != 0 {
		t.Fatal(got, ok, e)
	}
	if e = s.CheckPolicy("v2", now.Add(61*time.Minute)); e != nil {
		t.Fatal(e)
	}
}
func TestCorruptStateAndFailedPersistenceFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases")
	os.WriteFile(path, []byte("broken"), 0600)
	if s, e := Open(path); e == nil {
		s.Close()
		t.Fatal("corrupt state was accepted")
	}
	os.Remove(path)
	os.Remove(path + ".lock")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	os.Remove(path)
	os.Mkdir(path, 0700)
	_, _, e = s.Acquire("alice", "v1", map[string]bool{"a": true}, testCandidates("a"), time.Now())
	if e == nil {
		t.Fatal("persistence failure accepted")
	}
	if len(s.leases) != 0 {
		t.Fatal("failed allocation retained memory lease")
	}
}

func TestMissingStateIsNotTreatedAsAnEmptyPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if s, e = Open(path); e == nil {
		s.Close()
		t.Fatal("missing state reset all leases")
	}
}

func testCandidates(groups ...string) []Candidate {
	out := []Candidate{}
	for _, g := range groups {
		out = append(out, Candidate{Group: g, Credential: "account-" + g})
	}
	return out
}

func TestEightAccountsInOneGroupServeEightUsers(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	allowed := map[string]bool{"pool": true}
	candidates := make([]Candidate, 8)
	for i := range candidates {
		candidates[i] = Candidate{Group: "pool", Credential: fmt.Sprint("account-", i)}
	}
	var wg sync.WaitGroup
	results := make(chan Lease, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, done, err := s.Acquire(fmt.Sprint("user-", i), "v1", allowed, candidates, now)
			if err != nil {
				t.Error(err)
				return
			}
			defer done()
			results <- l
		}(i)
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for l := range results {
		if l.Credential == "" || seen[l.Credential] || l.Group != "pool" {
			t.Fatal("account shared", l)
		}
		seen[l.Credential] = true
	}
	if len(seen) != 8 {
		t.Fatal(len(seen))
	}
	if _, _, err := s.Acquire("ninth", "v1", allowed, candidates, now); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	before, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	l, done, err := s.Acquire("user-0", "v1", allowed, nil, now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	done()
	after, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !l.Expires.Equal(now.Add(time.Hour)) {
		t.Fatal("reuse rewrote or renewed lease")
	}
}

func TestLegacyLeaseNarrowsWithoutRenewalAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases")
	now := time.Now().UTC()
	legacy := Lease{ID: "old", Owner: "alice", Group: "pool", Policy: "v1", Started: now, Expires: now.Add(time.Hour)}
	raw, _ := json.Marshal(struct {
		Version int     `json:"version"`
		Leases  []Lease `json:"leases"`
	}{1, []Lease{legacy}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"pool": true}
	candidates := []Candidate{{"pool", "one"}, {"pool", "two"}}
	if _, _, err = s.Acquire("bob", "v1", allowed, candidates, now); !errors.Is(err, ErrBusy) {
		t.Fatal("legacy pool exposed", err)
	}
	// Failed narrowing must not free the legacy reservation.
	backup := s.path
	s.path = filepath.Join(t.TempDir(), "missing", "state")
	if _, _, err = s.Acquire("alice", "v1", allowed, candidates, now); err == nil {
		t.Fatal("ignored persistence failure")
	}
	s.path = backup
	l, ok, err := s.Lookup("alice", "v1", now)
	if err != nil || !ok || !l.LegacyGroup || l.Credential != "" {
		t.Fatal(l, err)
	}
	l, done, err := s.Acquire("alice", "v1", allowed, candidates, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	done()
	if l.Credential != "one" || l.LegacyGroup || l.ID != legacy.ID || !l.Expires.Equal(legacy.Expires) {
		t.Fatal(l)
	}
	b, done, err := s.Acquire("bob", "v1", allowed, candidates, now)
	if err != nil {
		t.Fatal(err)
	}
	done()
	if b.Credential != "two" {
		t.Fatal(b)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, ok, err = s.Lookup("alice", "v1", now)
	if err != nil || !ok || l.Credential != "one" {
		t.Fatal(l, err)
	}
}

func TestStateRejectsDuplicateAccountReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases")
	now := time.Now().UTC()
	a := Lease{ID: "a", Owner: "alice", Group: "pool", Credential: "same", Policy: "v1", Started: now, Expires: now.Add(time.Hour)}
	b := a
	b.ID = "b"
	b.Owner = "bob"
	raw, _ := json.Marshal(struct {
		Version int     `json:"version"`
		Leases  []Lease `json:"leases"`
	}{2, []Lease{a, b}})
	os.WriteFile(path, raw, 0600)
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("duplicate account accepted")
	}
}

func TestReplacementPreservesExpiryAndExclusiveOwnership(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	allowed := map[string]bool{"a": true, "b": true}
	candidates := []Candidate{{"a", "one"}, {"a", "two"}, {"a", "three"}, {"b", "other"}}
	old, done, err := s.Acquire("alice", "v1", allowed, candidates, now)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	_, done2, _ := s.Acquire("bob", "v1", allowed, candidates, now)
	defer done2()
	_, parallel, _ := s.Acquire("alice", "v1", allowed, candidates, now)
	if _, _, err = s.Replace(old, candidates, now); !errors.Is(err, ErrBusy) {
		t.Fatal("replaced during concurrent execution", err)
	}
	parallel()
	next, release, err := s.Replace(old, candidates, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if next.Credential != "three" || next.Group != old.Group || !next.Expires.Equal(old.Expires) || next.ID == old.ID {
		t.Fatalf("bad replacement %+v", next)
	}
	done()
	if rows := s.Snapshot(now); len(rows) != 2 {
		t.Fatal(rows)
	}
	if _, _, err = s.Replace(old, candidates, now); !errors.Is(err, ErrBusy) {
		t.Fatal("stale replacement accepted")
	}
	if _, _, err = s.Replace(next, []Candidate{{"a", "two"}, {"b", "other"}}, now); !errors.Is(err, ErrBusy) {
		t.Fatal("cross group or occupied replacement")
	}
	release()
}

func TestReplacementPersistenceFailureRestoresOldLease(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	c := []Candidate{{"a", "one"}, {"a", "two"}}
	old, done, _ := s.Acquire("alice", "v1", map[string]bool{"a": true}, c, now)
	defer done()
	original := s.path
	s.path = filepath.Join(t.TempDir(), "missing", "state")
	if _, _, err := s.Replace(old, c, now); err == nil {
		t.Fatal("expected save failure")
	}
	s.path = original
	rows := s.Snapshot(now)
	if len(rows) != 1 || rows[0].ID != old.ID || rows[0].Credential != old.Credential {
		t.Fatal(rows)
	}
}
