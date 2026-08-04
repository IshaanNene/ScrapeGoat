package antibot

import (
	"math"
	"math/rand"
	"time"
)

// HumanBehaviour simulates human-like interaction patterns to avoid bot detection.
type HumanBehaviour struct {
	rng *rand.Rand
}

// NewHumanBehaviour creates a human behaviour simulator.
func NewHumanBehaviour() *HumanBehaviour {
	return &HumanBehaviour{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// MousePath generates a realistic mouse movement path between two points.
// Returns a slice of (x, y, timestamp_ms) tuples.
func (h *HumanBehaviour) MousePath(fromX, fromY, toX, toY int) [][3]int {
	distance := math.Sqrt(float64((toX-fromX)*(toX-fromX) + (toY-fromY)*(toY-fromY)))
	steps := int(math.Max(5, distance/20)) // ~20px per step.

	path := make([][3]int, 0, steps)
	startTime := 0

	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		// Ease-in-out cubic for natural movement.
		ease := t * t * (3 - 2*t)

		x := float64(fromX) + (float64(toX-fromX) * ease)
		y := float64(fromY) + (float64(toY-fromY) * ease)

		// Add small random jitter.
		x += float64(h.rng.Intn(5) - 2)
		y += float64(h.rng.Intn(5) - 2)

		// Time between steps: faster in middle, slower at start/end.
		dt := 10 + h.rng.Intn(20) // 10-30ms per step
		startTime += dt

		path = append(path, [3]int{int(x), int(y), startTime})
	}

	return path
}

// ScrollDelay returns a random delay between scrolls (ms).
func (h *HumanBehaviour) ScrollDelay() time.Duration {
	// Humans scroll in bursts with pauses.
	base := 200 + h.rng.Intn(800) // 200-1000ms
	return time.Duration(base) * time.Millisecond
}

// ScrollAmount returns a random scroll amount (px).
func (h *HumanBehaviour) ScrollAmount() int {
	// Most scrolls are 100-400px with occasional larger jumps.
	if h.rng.Float64() < 0.1 {
		return 500 + h.rng.Intn(500) // Occasional big scroll.
	}
	return 100 + h.rng.Intn(300) // Normal scroll.
}

// TypingSpeed returns realistic inter-key delays for typing.
func (h *HumanBehaviour) TypingSpeed(text string) []time.Duration {
	delays := make([]time.Duration, len(text))
	for i := range text {
		base := 50 + h.rng.Intn(100) // 50-150ms base.

		// Slow down at word boundaries.
		if i > 0 && text[i] == ' ' {
			base += 50 + h.rng.Intn(100)
		}

		// Occasional longer pause (thinking).
		if h.rng.Float64() < 0.05 {
			base += 200 + h.rng.Intn(500)
		}

		delays[i] = time.Duration(base) * time.Millisecond
	}
	return delays
}

// ReadDelay returns a realistic reading delay based on content length.
func (h *HumanBehaviour) ReadDelay(contentLength int) time.Duration {
	// Average reading speed: ~200 words/min, ~1000 chars/min.
	readTimeMs := float64(contentLength) / 1000.0 * 60000.0 / 200.0
	// Add random variation (±30%).
	variation := readTimeMs * 0.3 * (h.rng.Float64()*2 - 1)
	return time.Duration(readTimeMs+variation) * time.Millisecond
}

// RandomViewport returns a realistic viewport size.
func (h *HumanBehaviour) RandomViewport() (width, height int) {
	viewports := [][2]int{
		{1920, 1080}, {1366, 768}, {1536, 864}, {1440, 900},
		{1280, 720}, {2560, 1440}, {1600, 900}, {1280, 1024},
	}
	vp := viewports[h.rng.Intn(len(viewports))]
	return vp[0], vp[1]
}

// RandomTimezone returns a random common timezone.
func (h *HumanBehaviour) RandomTimezone() string {
	timezones := []string{
		"America/New_York", "America/Chicago", "America/Denver",
		"America/Los_Angeles", "Europe/London", "Europe/Paris",
		"Europe/Berlin", "Asia/Tokyo", "Asia/Shanghai",
		"Australia/Sydney", "America/Toronto",
	}
	return timezones[h.rng.Intn(len(timezones))]
}
