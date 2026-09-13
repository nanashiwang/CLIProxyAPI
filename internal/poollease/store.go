// Package poollease implements persistent, single-process exclusive account leases.
package poollease

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var ErrBusy = errors.New("pool_busy")
var ErrDenied = errors.New("pool_lease_access_denied")
var ErrPolicy = errors.New("pool_lease_policy_changed")

type Candidate struct {
	Group      string
	Credential string
}

type Lease struct {
	Reassigned  bool      `json:"reassigned,omitempty"`
	Credential  string    `json:"credential,omitempty"`
	LegacyGroup bool      `json:"legacy_group,omitempty"`
	ID          string    `json:"id"`
	Owner       string    `json:"owner"`
	Group       string    `json:"group"`
	Policy      string    `json:"policy"`
	Started     time.Time `json:"started_at"`
	Expires     time.Time `json:"expires_at"`
	Active      int       `json:"-"`
}
type Store struct {
	mu     sync.Mutex
	path   string
	lock   *os.File
	leases map[string]*Lease
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	_, markerErr := os.Stat(path + ".lock")
	initialized := markerErr == nil
	lock, err := lockFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, lock: lock, leases: map[string]*Lease{}}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		s.Close()
		return nil, err
	}
	if os.IsNotExist(err) {
		if initialized {
			s.Close()
			return nil, errors.New("pool lease state is missing; restore it before routing")
		}
		if err = s.save(); err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	}
	if err == nil {
		var state struct {
			Version int     `json:"version"`
			Leases  []Lease `json:"leases"`
		}
		if err = json.Unmarshal(raw, &state); err != nil || (state.Version != 1 && state.Version != 2) {
			s.Close()
			return nil, errors.New("invalid pool lease state")
		}
		owners := map[string]bool{}
		credentials := map[string]bool{}
		groups := map[string]bool{}
		legacyGroups := map[string]bool{}
		for _, l := range state.Leases {
			if state.Version == 1 {
				if l.Credential != "" {
					s.Close()
					return nil, errors.New("invalid legacy lease")
				}
				l.LegacyGroup = true
			}
			invalidResource := (l.Credential == "") != l.LegacyGroup
			overlap := legacyGroups[l.Group] || (l.LegacyGroup && groups[l.Group]) || (l.Credential != "" && credentials[l.Credential])
			if l.ID == "" || l.Owner == "" || l.Group == "" || l.Policy == "" || !l.Expires.After(l.Started) || s.leases[l.ID] != nil || owners[l.Owner] || invalidResource || overlap {
				s.Close()
				return nil, errors.New("invalid pool lease record")
			}
			copy := l
			s.leases[l.ID] = &copy
			owners[l.Owner] = true
			groups[l.Group] = true
			if l.LegacyGroup {
				legacyGroups[l.Group] = true
			} else {
				credentials[l.Credential] = true
			}
		}
	}
	return s, nil
}
func (s *Store) Close() error { return s.lock.Close() }
func (s *Store) prune(now time.Time) {
	for g, l := range s.leases {
		if !now.Before(l.Expires) && l.Active == 0 {
			delete(s.leases, g)
		}
	}
}
func (s *Store) check(policy string, now time.Time) error {
	s.prune(now)
	for _, l := range s.leases {
		if l.Policy != policy {
			return ErrPolicy
		}
	}
	return nil
}
func (s *Store) CheckPolicy(policy string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.check(policy, now)
}
func (s *Store) Lookup(owner, policy string, now time.Time) (Lease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(policy, now); err != nil {
		return Lease{}, false, err
	}
	for _, l := range s.leases {
		if l.Owner == owner {
			return *l, true, nil
		}
	}
	return Lease{}, false, nil
}

// occupied protects account IDs globally; legacy v1 records temporarily protect
// their entire group until the owner safely narrows the lease or it expires.
func (s *Store) occupied(candidate Candidate, ignore string) bool {
	for _, l := range s.leases {
		if l.ID != ignore && (l.Credential == candidate.Credential || (l.LegacyGroup && l.Group == candidate.Group)) {
			return true
		}
	}
	return false
}
func (s *Store) Acquire(owner, policy string, allowed map[string]bool, candidates []Candidate, now time.Time) (Lease, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(policy, now); err != nil {
		return Lease{}, nil, err
	}
	for _, l := range s.leases {
		if l.Owner != owner {
			continue
		}
		if !allowed[l.Group] {
			return Lease{}, nil, ErrDenied
		}
		if !now.Before(l.Expires) {
			return Lease{}, nil, ErrBusy
		}
		if l.LegacyGroup {
			if l.Active != 0 {
				return Lease{}, nil, ErrBusy
			}
			chosen := ""
			for _, c := range candidates {
				if c.Group == l.Group && c.Credential != "" && !s.occupied(c, l.ID) {
					chosen = c.Credential
					break
				}
			}
			if chosen == "" {
				return Lease{}, nil, ErrBusy
			}
			l.Credential = chosen
			l.LegacyGroup = false
			if err := s.save(); err != nil {
				l.Credential = ""
				l.LegacyGroup = true
				return Lease{}, nil, err
			}
		}
		l.Active++
		return *l, s.releaser(l.ID), nil
	}
	for _, c := range candidates {
		if c.Credential == "" || !allowed[c.Group] || s.occupied(c, "") {
			continue
		}
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return Lease{}, nil, err
		}
		l := &Lease{ID: hex.EncodeToString(id), Owner: owner, Group: c.Group, Credential: c.Credential, Policy: policy, Started: now, Expires: now.Add(time.Hour), Active: 1}
		s.leases[l.ID] = l
		if err := s.save(); err != nil {
			delete(s.leases, l.ID)
			return Lease{}, nil, err
		}
		return *l, s.releaser(l.ID), nil
	}
	return Lease{}, nil, ErrBusy
}
func (s *Store) releaser(id string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if l := s.leases[id]; l != nil {
				l.Active--
			}
		})
	}
}
func (s *Store) save() error {
	rows := make([]Lease, 0, len(s.leases))
	for _, l := range s.leases {
		rows = append(rows, *l)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Group < rows[j].Group || (rows[i].Group == rows[j].Group && rows[i].ID < rows[j].ID)
	})
	raw, err := json.Marshal(struct {
		Version int     `json:"version"`
		Leases  []Lease `json:"leases"`
	}{2, rows})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".pool-lease-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return fmt.Errorf("persist pool lease: %w", err)
	}
	return nil
}

func (s *Store) Snapshot(now time.Time) []Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	rows := make([]Lease, 0, len(s.leases))
	for _, l := range s.leases {
		rows = append(rows, *l)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Group < rows[j].Group || (rows[i].Group == rows[j].Group && rows[i].ID < rows[j].ID)
	})
	return rows
}

// Replace moves a lease only when its sole in-flight request has stopped using
// the old credential. Rotating the ID invalidates old routing namespaces.
func (s *Store) Replace(expected Lease, candidates []Candidate, now time.Time) (Lease, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(expected.Policy, now); err != nil {
		return Lease{}, nil, err
	}
	old := s.leases[expected.ID]
	if old == nil || old.Owner != expected.Owner || old.Credential != expected.Credential || old.LegacyGroup || old.Active != 1 || !now.Before(old.Expires) {
		return Lease{}, nil, ErrBusy
	}
	for _, c := range candidates {
		if c.Group != old.Group || c.Credential == "" || c.Credential == old.Credential || s.occupied(c, "") {
			continue
		}
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return Lease{}, nil, err
		}
		next := *old
		next.ID, next.Credential, next.Reassigned = hex.EncodeToString(id), c.Credential, true
		delete(s.leases, old.ID)
		s.leases[next.ID] = &next
		if err := s.save(); err != nil {
			delete(s.leases, next.ID)
			s.leases[old.ID] = old
			return Lease{}, nil, err
		}
		return next, s.releaser(next.ID), nil
	}
	return Lease{}, nil, ErrBusy
}

// Hold retains a live lease while an abandoned upstream producer is drained.
func (s *Store) Hold(id string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.leases[id]
	if l == nil || l.Active == 0 {
		return nil, false
	}
	l.Active++
	return s.releaser(id), true
}
