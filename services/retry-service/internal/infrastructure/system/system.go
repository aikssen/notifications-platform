package system

import (
	"math/rand"
	"time"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/application/port"
)

type Clock struct{}

func (Clock) Now() time.Time { return time.Now().UTC() }

// Randomizer supplies the backoff jitter. Not a security decision, so the
// cheap generator is the right one.
type Randomizer struct{}

func (Randomizer) Float64() float64 {
	//nolint:gosec // jitter only
	return rand.Float64()
}

var (
	_ port.Clock      = Clock{}
	_ port.Randomizer = Randomizer{}
)
