package ids

import "github.com/google/uuid"

// New returns a time-ordered UUIDv7 string, which keeps SQLite indexes and the
// audit trail roughly chronological.
func New() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}
