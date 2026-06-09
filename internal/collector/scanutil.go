package collector

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"toktop.unceas.dev/internal/trace"
)

// FinalizeCounts fills an Index's denormalized count fields from its slices.
// Shared by every collector's ingest path (was duplicated verbatim per provider).
func FinalizeCounts(index *trace.Index) {
	index.SessionCount = len(index.Sessions)
	index.TurnCount = len(index.Turns)
	index.InvocationCount = len(index.Invocations)
	index.SubagentCount = len(index.SubagentRuns)
	count := 0
	for _, turn := range index.Turns {
		count += len(turn.ToolCalls)
	}
	index.ToolCallCount = count
}

func HashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func StatFingerprint(path string) (size int64, mtimeNS int64, ino uint64, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, false
	}
	return info.Size(), info.ModTime().UnixNano(), fileInode(info), true
}

func ReadFileOK(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

func MCPServerID(scope, configPath, name string) string {
	return trace.ID16("mcp", scope, configPath, name)
}

// SkillID is the stable 16-hex id for an installed skill at scope/path. Like
// MCPServerID it delegates to trace.ID16 so skill ids share the one canonical
// hashing/framing scheme instead of a hand-rolled, divergent copy.
func SkillID(scope, path string) string {
	return trace.ID16("skill", scope, path)
}

// ClassifyMCPTransport infers an MCP server's transport from its declared type
// and connection fields: a recognized type (stdio|http|sse) wins, else a url
// implies http and a command implies stdio, else "unknown". One definition for
// both providers (codex passes TOML fields, claude-code unmarshals JSON first).
func ClassifyMCPTransport(typ, url, command string) string {
	switch strings.ToLower(typ) {
	case "stdio", "http", "sse":
		return strings.ToLower(typ)
	}
	if url != "" {
		return "http"
	}
	if command != "" {
		return "stdio"
	}
	return "unknown"
}

// AppendUniqueMCPServers appends servers to *out, skipping any whose
// (Scope, Name, ConfigPath) key is already in seen. The dedup key lives here once
// so both providers can't drift apart on it.
func AppendUniqueMCPServers(out *[]trace.MCPServer, seen map[string]struct{}, servers ...trace.MCPServer) {
	for _, server := range servers {
		key := server.Scope + "|" + server.Name + "|" + server.ConfigPath
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		*out = append(*out, server)
	}
}

// AppendUniqueSkills appends skills to *out, skipping any whose
// (Scope, Name, SourcePath) key is already in seen.
func AppendUniqueSkills(out *[]trace.Skill, seen map[string]struct{}, skills ...trace.Skill) {
	for _, skill := range skills {
		key := skill.Scope + "|" + skill.Name + "|" + skill.SourcePath
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		*out = append(*out, skill)
	}
}

func UniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = filepath.Clean(v)
		if v == "." || v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

const jsonlReaderBufBytes = 256 * 1024

// jsonlReaderPool reuses the bufio.Reader ReadJSONLLines needs per file. A full
// ingest reads ~1000 files in parallel (collect runs under SafeMapErr), and a
// fresh 256KiB reader per file was ~243MB of transient allocation; pooling reuses
// them. sync.Pool is goroutine-safe and each ReadJSONLLines call owns its reader
// for the call's duration (Get→Reset→use→Put), never sharing one mid-read.
var jsonlReaderPool = sync.Pool{
	New: func() any { return bufio.NewReaderSize(nil, jsonlReaderBufBytes) },
}

func ReadJSONLLines(ctx context.Context, r io.Reader, visit func(lineNo int, byteOffset int64, line []byte) error) error {
	reader := jsonlReaderPool.Get().(*bufio.Reader)
	reader.Reset(r)
	defer func() {
		// Drop the io.Reader reference before returning to the pool so a closed
		// *os.File isn't pinned until the reader's next reuse.
		reader.Reset(nil)
		jsonlReaderPool.Put(reader)
	}()
	lineNo := 0
	var pending []byte
	var offset int64
	var lineStart int64
	pendingActive := false
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("collect cancelled: %w", err)
		}
		fragment, err := reader.ReadSlice('\n')
		if !pendingActive {
			lineStart = offset
		}
		offset += int64(len(fragment))
		if errors.Is(err, bufio.ErrBufferFull) {
			pending = append(pending, fragment...)
			pendingActive = true
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		line := fragment
		if len(pending) > 0 {
			pending = append(pending, fragment...)
			line = pending
			pending = nil
		}
		pendingActive = false
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) > 0 {
			lineNo++
			if err := visit(lineNo, lineStart, bytes.Clone(trimmed)); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}
