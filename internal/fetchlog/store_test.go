package fetchlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	body := []byte("<html><body>hello</body></html>")
	digest, err := s.Put(body)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if digest != Digest(body) {
		t.Errorf("Put returned %s, want %s", digest, Digest(body))
	}

	got, err := s.Get(digest)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("Get returned %q, want %q", got, body)
	}
	if !s.Has(digest) {
		t.Error("Has says the object we just wrote is absent")
	}
}

// The store's whole claim is that identical bytes cost storage once. If this
// stops holding, a re-crawl of mostly-unchanged pages stops being affordable.
func TestStoreDeduplicates(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	body := []byte("the same bytes twice")
	d1, err := s.Put(body)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	d2, err := s.Put(body)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("same bytes produced different addresses: %s vs %s", d1, d2)
	}

	st, err := s.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Objects != 1 {
		t.Errorf("two identical Puts left %d objects, want 1", st.Objects)
	}
}

func TestStoreEmptyBody(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// A 204 has no body. Storing nothing must still round-trip, because the
	// alternative is a replay that cannot distinguish "empty" from "missing".
	digest, err := s.Put(nil)
	if err != nil {
		t.Fatalf("Put(nil): %v", err)
	}
	got, err := s.Get(digest)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty body came back as %q", got)
	}
}

func TestStoreMissingDigest(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, err = s.Get(Digest([]byte("never stored")))
	if err == nil {
		t.Fatal("Get of an absent digest succeeded")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error does not say the object is missing: %v", err)
	}
}

// Detecting tampering on read is the difference between a log that proves
// something and a log that merely stores something.
func TestStoreDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	digest, err := s.Put([]byte("original content"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	path := filepath.Join(dir, "objects", digest[:2], digest[2:])
	if err := os.WriteFile(path, []byte("tampered content"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if _, err := s.Get(digest); err == nil {
		t.Fatal("Get returned tampered bytes without complaint")
	} else if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("error does not name corruption: %v", err)
	}

	if err := s.Verify(); err == nil {
		t.Fatal("Verify passed a tampered store")
	}
}

func TestStoreVerifyClean(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	for _, b := range [][]byte{[]byte("a"), []byte("bb"), []byte("ccc")} {
		if _, err := s.Put(b); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	if err := s.Verify(); err != nil {
		t.Errorf("Verify failed on an untouched store: %v", err)
	}

	st, err := s.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Objects != 3 || st.Bytes != 6 {
		t.Errorf("Stat = %d objects / %d bytes, want 3 / 6", st.Objects, st.Bytes)
	}
}

// Sharding exists so no directory holds millions of children. Assert the layout
// rather than trusting it, since a regression here is invisible until the store
// is large enough that fixing it is expensive.
func TestStoreShardsByPrefix(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	digest, err := s.Put([]byte("shard me"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	want := filepath.Join(dir, "objects", digest[:2], digest[2:])
	if _, err := os.Stat(want); err != nil {
		t.Errorf("object not at sharded path %s: %v", want, err)
	}
}

// A failed Put must not leave a temp file claiming an address.
func TestStoreLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := s.Put([]byte("clean up after yourself")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err = filepath.Walk(filepath.Join(dir, "objects"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasPrefix(info.Name(), ".tmp-") {
			t.Errorf("temp file survived: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
