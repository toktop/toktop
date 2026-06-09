package trace

import "time"

// ParseEventTime parses an RFC3339Nano transcript timestamp into the neutral
// trace domain, returning the zero time on empty input or any parse error. It is
// the one canonical event-time parse shared by every collector's line decoder
// and every parser, alongside the canonical IDs/hashes in this package — instead
// of the byte-identical copies the collector and parser layers each carried.
func ParseEventTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
