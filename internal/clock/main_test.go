package clock

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if a test leaves a goroutine running.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
