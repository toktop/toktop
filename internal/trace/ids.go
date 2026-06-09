package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

func SourceID(kind string) string {
	return id16("source", kind)
}

func SourceRootID(sourceID, path string) string {
	return id16("source_root", sourceID, path)
}

func SessionID(sourceRootID, transcriptPath string) string {
	return id16("session", sourceRootID, transcriptPath)
}

func TurnID(sessionID string, turnIndex int) string {
	return id16("turn", sessionID, strconv.Itoa(turnIndex))
}

func InvocationID(turnID string, invocationIndex int) string {
	return id16("invocation", turnID, strconv.Itoa(invocationIndex))
}

func ToolCallID(turnID, useID string, callIndex int) string {
	return id16("tool_call", turnID, useID, strconv.Itoa(callIndex))
}

func RawEventID(sourceRootID, sourceFile string, lineNo int, rawHash string) string {
	// raw_events.id is a TEXT PRIMARY KEY on the highest-volume table; a 64-bit
	// truncation risks birthday-bound PK collisions (which abort the whole ingest
	// transaction). Use the full SHA-256 here to make collisions infeasible.
	return idN(64, "raw_event", sourceRootID, sourceFile, strconv.Itoa(lineNo), rawHash)
}

func ProjectID(sourceID, name, path string) string {
	return id16("project", sourceID, name, path)
}

func id16(parts ...string) string {
	return idN(16, parts...)
}

// ID16 is the exported canonical content-ID over the null-delimited parts, so
// other packages (e.g. collector) share one hashing/framing scheme instead of
// hand-rolling a divergent one that could silently drift from these IDs.
func ID16(parts ...string) string {
	return id16(parts...)
}

// idN returns the first n hex characters of the SHA-256 over the
// null-delimited parts. n must not exceed 64 (the full hex digest width).
func idN(n int, parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))[:n]
}

func HashPayload(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
