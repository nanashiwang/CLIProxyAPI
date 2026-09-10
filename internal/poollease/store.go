// Package poollease implements persistent, single-process exclusive pool leases.
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

type Lease struct {
	ID      string    `json:"id"`
	Owner   string    `json:"owner"`
	Group   string    `json:"group"`
	Policy  string    `json:"policy"`
	Started time.Time `json:"started_at"`
	Expires time.Time `json:"expires_at"`
	Active  int       `json:"-"`
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
		if err = json.Unmarshal(raw, &state); err != nil || state.Version != 1 {
			s.Close()
			return nil, errors.New("invalid pool lease state")
		}
		owners := map[string]bool{}
		for _, l := range state.Leases {
			if l.ID == "" || l.Owner == "" || l.Group == "" || l.Policy == "" || !l.Expires.After(l.Started) || s.leases[l.Group] != nil || owners[l.Owner] {
				s.Close()
				return nil, errors.New("invalid pool lease record")
			}
			copy := l
			s.leases[l.Group] = &copy
			owners[l.Owner] = true
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
func (s *Store) Acquire(owner, policy string, allowed map[string]bool, candidates []string, now time.Time) (Lease, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(policy, now); err != nil {
		return Lease{}, nil, err
	}
	for _, l := range s.leases {
		if l.Owner == owner {
			if !allowed[l.Group] {
				return Lease{}, nil, ErrDenied
			}
			if !now.Before(l.Expires) {
				return Lease{}, nil, ErrBusy
			}
			l.Active++
			return *l, s.releaser(l.Group, l.ID), nil
		}
	}
	for _, group := range candidates {
		if !allowed[group] || s.leases[group] != nil {
			continue
		}
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return Lease{}, nil, err
		}
		l := &Lease{ID: hex.EncodeToString(id), Owner: owner, Group: group, Policy: policy, Started: now, Expires: now.Add(time.Hour), Active: 1}
		s.leases[group] = l
		if err := s.save(); err != nil {
			delete(s.leases, group)
			return Lease{}, nil, err
		}
		return *l, s.releaser(group, l.ID), nil
	}
	return Lease{}, nil, ErrBusy
}
func (s *Store) releaser(group, id string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if l := s.leases[group]; l != nil && l.ID == id {
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
	sort.Slice(rows, func(i, j int) bool { return rows[i].Group < rows[j].Group })
	raw, err := json.Marshal(struct {
		Version int     `json:"version"`
		Leases  []Lease `json:"leases"`
	}{1, rows})
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
	sort.Slice(rows, func(i, j int) bool { return rows[i].Group < rows[j].Group })
	return rows
}
