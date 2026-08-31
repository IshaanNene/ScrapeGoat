package safety

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if a test leaves a goroutine running.
//
// The guarded proxy splices two directions of a CONNECT tunnel with a goroutine
// each and relies on deferred Closes to tear down the one that did not finish
// first. That is the kind of invariant that holds until it quietly does not, and
// a leaked splice pair per blocked fetch is invisible in a passing test suite.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
