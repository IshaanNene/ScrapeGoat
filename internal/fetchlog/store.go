// Package fetchlog records what a crawl fetched, so the crawl can be replayed.
//
// This is the substrate for three things that a crawler cannot otherwise offer:
// a dataset a third party can verify, concurrency bugs that reproduce on demand,
// and crawler policies that can be compared on identical input. See
// docs/design/0001-deterministic-crawl.md for why those three follow from one
// architectural decision rather than three.
//
// # Layout
//
//	<dir>/objects/ab/cdef...   response bodies, keyed by SHA-256 of their bytes
//	<dir>/index.jsonl          the ledger: one line per fetch attempt
//	<dir>/manifest.json        crawl identity: config hash, seed, code version
//
// Bodies are content-addressed so that identical bytes are stored once. This is
// not a micro-optimisation: a re-crawl is mostly unchanged pages, and storing
// each one again on every pass is the difference between a log you keep and a log
// you delete.
//
// The index is JSONL rather than a database. It is append-only, so a crash
// truncates at a record boundary rather than corrupting; it streams, so a
// billion-entry log does not need to fit in memory; and it can be read with grep
// when something has gone wrong at three in the morning, which is worth more than
// query planning for a write-once ledger.
package fetchlog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned when a digest is absent from the store.
var ErrNotFound = errors.New("fetchlog: object not found")

// Store holds response bodies, addressed by the SHA-256 of their content.
type Store struct {
	dir string
}

// NewStore opens or creates a content-addressed store under dir.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		return nil, fmt.Errorf("fetchlog: create store: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Digest returns the address of a body without storing it.
func Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// path splits the digest so that no directory holds millions of entries — many
// filesystems degrade badly past a few tens of thousands of children.
func (s *Store) path(digest string) string {
	return filepath.Join(s.dir, "objects", objectRel(digest))
}

// objectRel is the object's path relative to the objects directory.
func objectRel(digest string) string {
	if len(digest) < 4 {
		return digest
	}
	return filepath.Join(digest[:2], digest[2:])
}

// openObjects opens the objects directory as a root.
//
// Every read of a stored body goes through this rather than through a plain
// os.Open on a joined path. The reason is the threat model this package exists to
// serve: a log is meant to be checkable by someone who does not trust whoever
// published it, so the directory itself is untrusted input. filepath.Walk uses
// Lstat and will not traverse a symlink, but os.Open follows one — so a log
// carrying a symlink at objects/ab/cdef… would have the verifier hash a file
// outside the store. That is not merely an information leak: point the link at a
// file whose hash you already know and a fabricated object verifies clean, which
// is precisely the guarantee Verify is supposed to provide. os.Root refuses to
// escape, so the question does not arise.
func (s *Store) openObjects() (*os.Root, error) {
	root, err := os.OpenRoot(filepath.Join(s.dir, "objects"))
	if err != nil {
		return nil, fmt.Errorf("fetchlog: open store: %w", err)
	}
	return root, nil
}

// Put stores a body and returns its digest.
//
// Writing an object that already exists is a no-op rather than an error: the same
// bytes produce the same address, so a duplicate is proof the store is working.
func (s *Store) Put(body []byte) (string, error) {
	digest := Digest(body)
	path := s.path(digest)

	if _, err := os.Stat(path); err == nil {
		return digest, nil // already present; identical by construction
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("fetchlog: create object dir: %w", err)
	}

	// Write to a temporary file and rename, so a crash mid-write cannot leave a
	// truncated object sitting at an address that claims to hold complete bytes.
	// A corrupt object is worse than a missing one: the digest asserts content
	// that is not there, and every later verification inherits the lie.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("fetchlog: temp object: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("fetchlog: write object: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("fetchlog: sync object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("fetchlog: close object: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("fetchlog: commit object: %w", err)
	}

	return digest, nil
}

// Get retrieves a body by digest.
//
// The content is verified against its address on read. A store whose bytes have
// been altered on disk would otherwise replay silently wrong data, which defeats
// the entire point of recording them.
func (s *Store) Get(digest string) ([]byte, error) {
	root, err := s.openObjects()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	f, err := root.Open(objectRel(digest))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, digest)
		}
		return nil, fmt.Errorf("fetchlog: read object: %w", err)
	}
	defer f.Close()

	body, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("fetchlog: read object: %w", err)
	}

	if got := Digest(body); got != digest {
		return nil, fmt.Errorf("fetchlog: object %s is corrupt (content hashes to %s)",
			digest, got)
	}
	return body, nil
}

// Has reports whether a digest is present, without reading it.
func (s *Store) Has(digest string) bool {
	_, err := os.Stat(s.path(digest))
	return err == nil
}

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.dir }

// Stats describes what a store holds.
type Stats struct {
	Objects int
	Bytes   int64
}

// Stat walks the store. Linear in the number of objects, so it is a diagnostic
// rather than something to call in a loop.
func (s *Store) Stat() (Stats, error) {
	var st Stats
	root := filepath.Join(s.dir, "objects")

	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		st.Objects++
		st.Bytes += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return st, fmt.Errorf("fetchlog: stat store: %w", err)
	}
	return st, nil
}

// Verify checks every object against its address.
//
// This is what makes a published dataset checkable: anyone holding the log can
// confirm that no byte has changed since it was written, without trusting whoever
// wrote it.
func (s *Store) Verify() error {
	root, err := s.openObjects()
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing stored yet
		}
		return err
	}
	defer root.Close()

	return fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}

		// Anything that is not a regular file cannot be an object. Skipping rather
		// than following is the point: a symlink planted in a published log must
		// not be dereferenced by the tool that is supposed to be auditing it.
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("fetchlog: %s is not a regular file; a log should contain only objects", path)
		}

		// Reconstruct the digest from the path: <2>/<rest>.
		digest := strings.ReplaceAll(path, "/", "")

		f, err := root.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}

		if got := hex.EncodeToString(h.Sum(nil)); got != digest {
			return fmt.Errorf("fetchlog: object %s is corrupt (hashes to %s)", digest, got)
		}
		return nil
	})
}
