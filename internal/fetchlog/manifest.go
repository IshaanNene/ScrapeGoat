package fetchlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Manifest is the crawl's identity: enough to say what produced this log and
// under what conditions, without which the log records bytes but not provenance.
//
// The three hashes are the interesting part. Two logs with the same ConfigHash
// and Seed were crawled under identical policy and entropy, so any difference in
// their output came from the network. Two logs with different ConfigHashes are
// not comparable, and saying so is more useful than silently diffing them.
type Manifest struct {
	// Version of ScrapeGoat that wrote the log. Code is part of the input.
	Version string `json:"version"`

	// Seeds the crawl started from, in the order given.
	Seeds []string `json:"seeds"`

	// ConfigHash is the SHA-256 of the canonicalised configuration. Policy is the
	// other half of the input: the same seeds under different depth limits or
	// robots settings are different crawls.
	ConfigHash string `json:"config_hash"`

	// Config is the configuration itself, embedded so a replay is self-contained.
	// A log that recorded only the hash would let you detect that policy differed
	// but not reproduce the original — you would need the operator's config file,
	// which is exactly the thing that goes missing.
	Config json.RawMessage `json:"config,omitempty"`

	// Seed is the value the engine's random source was seeded with, when the
	// caller chose one. Zero means the run drew from the OS and its jitter is not
	// reproducible — a Tier 2 replay of that run cannot be exact.
	RandSeed uint64 `json:"rand_seed,omitempty"`

	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`

	// Entries and Objects are written at close, so a manifest with zeroes and no
	// FinishedAt is the signature of a crawl that was killed rather than one that
	// fetched nothing.
	Entries int64 `json:"entries"`
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

// HashConfig canonicalises a value and returns its SHA-256.
//
// JSON rather than a struct walk: encoding/json sorts map keys, so the encoding
// is stable across runs, and the hash stays meaningful when the config struct
// gains fields — a new field changes the hash, which is correct, because a crawl
// under a policy that has grown a knob is not the same crawl.
func HashConfig(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("fetchlog: hash config: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// WriteManifest writes the manifest to dir, replacing any existing one.
func WriteManifest(dir string, m Manifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fetchlog: create dir: %w", err)
	}

	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("fetchlog: encode manifest: %w", err)
	}
	b = append(b, '\n')

	// Same temp-and-rename as the object store: a half-written manifest would
	// misdescribe a log that is itself fine.
	path := filepath.Join(dir, "manifest.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("fetchlog: write manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("fetchlog: commit manifest: %w", err)
	}
	return nil
}

// ReadManifest loads the manifest from dir.
func ReadManifest(dir string) (Manifest, error) {
	var m Manifest

	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return m, fmt.Errorf("fetchlog: no manifest in %s", dir)
		}
		return m, fmt.Errorf("fetchlog: read manifest: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("fetchlog: parse manifest: %w", err)
	}
	return m, nil
}

// Complete reports whether the crawl that wrote this manifest finished. An
// incomplete log is still replayable — it just replays a crawl that was cut off,
// and a caller comparing two runs should know which it has.
func (m Manifest) Complete() bool { return !m.FinishedAt.IsZero() }
