package reservas

import (
	"time"
)

// parsed is what a message says about a booking, as the LLM reads it. Zero
// values mean "not said".
type parsed struct {
	related       bool // about the chair at all; chatter is ignored
	mentionsChair bool // starts a new request
	cancel        bool // drops the request in progress
	list          bool // wants the saved requests
	date          time.Time
	start, end    int // hours
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
