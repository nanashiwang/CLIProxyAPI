package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// StableIDGenerator generates stable, deterministic IDs for auth entries.
// It uses SHA256 hashing with collision handling via counters.
// It is not safe for concurrent use.
type StableIDGenerator struct {
	counters map[string]int
}

// NewStableIDGenerator creates a new StableIDGenerator instance.
func NewStableIDGenerator() *StableIDGenerator {
	return &StableIDGenerator{counters: make(map[string]int)}
}

// Next generates a stable ID based on the kind and parts.
// Returns the full ID (kind:hash) and the short hash portion.
func (g *StableIDGenerator) Next(kind string, parts ...string) (string, string) {
	if g == nil {
		return kind + ":000000000000", "000000000000"
	}
	hasher := sha256.New()
	hasher.Write([]byte(kind))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		hasher.Write([]byte{0})
		hasher.Write([]byte(trimmed))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if len(digest) < 12 {
		digest = fmt.Sprintf("%012s", digest)
	}
	short := digest[:12]
	key := kind + ":" + short
	index := g.counters[key]
	g.counters[key] = index + 1
	if index > 0 {
		short = fmt.Sprintf("%s-%d", short, index)
	}
	return fmt.Sprintf("%s:%s", kind, short), short
}
