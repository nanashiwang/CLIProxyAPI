package poollease

import (
	"errors"
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
			l, done, e := s.Acquire("alice", "v1", allowed, []string{"a", "b"}, now)
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
	l, done, e := s.Acquire("alice", "v1", allowed, []string{"b"}, now.Add(30*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if !l.Expires.Equal(now.Add(time.Hour)) {
		t.Fatal("request renewed the fixed lease")
	}
	done()
	b, done, e := s.Acquire("bob", "v1", allowed, []string{"a", "b"}, now)
	if e != nil {
		t.Fatal(e)
	}
	if b.Group == l.Group {
		t.Fatal("users shared pool")
	}
	done()
	_, _, e = s.Acquire("charlie", "v1", allowed, []string{"a", "b"}, now)
	if !errors.Is(e, ErrBusy) {
		t.Fatal(e)
	}
}
func TestExpiredInFlightLeaseCannotBeReassigned(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	allowed := map[string]bool{"a": true}
	_, done, e := s.Acquire("alice", "v1", allowed, []string{"a"}, now)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s.Acquire("bob", "v1", allowed, []string{"a"}, now.Add(61*time.Minute))
	if !errors.Is(e, ErrBusy) {
		t.Fatal("reassigned in-flight pool", e)
	}
	done()
	done()
	l, release, e := s.Acquire("bob", "v1", allowed, []string{"a"}, now.Add(61*time.Minute))
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
	l, done, e := s.Acquire("alice", "v1", map[string]bool{"a": true}, []string{"a"}, now)
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
	_, _, e = s.Acquire("alice", "v1", map[string]bool{"a": true}, []string{"a"}, time.Now())
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
