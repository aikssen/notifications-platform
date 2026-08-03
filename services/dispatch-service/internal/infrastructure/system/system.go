// Package system holds the two trivial adapters that exist only so the core
// never calls time.Now() or generates a UUID directly.
//
// They look like ceremony until you write a test that has to assert on a
// timestamp or an identifier.
package system

import (
	"time"

	"github.com/google/uuid"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
)

type Clock struct{}

func (Clock) Now() time.Time { return time.Now().UTC() }

type UUIDGenerator struct{}

func (UUIDGenerator) NewID() string { return uuid.NewString() }

var (
	_ port.Clock       = Clock{}
	_ port.IDGenerator = UUIDGenerator{}
)
