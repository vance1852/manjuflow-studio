// Package logging emits structured JSON records. Handlers, services and workers
// share one logger so every line can be correlated by request identifier.
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level orders the emitted severities.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// ParseLevel converts configuration text into a Level.
func ParseLevel(raw string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	default:
		return LevelInfo, fmt.Errorf("unknown log level %q", raw)
	}
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

// Logger writes one JSON object per record.
type Logger struct {
	mu     *sync.Mutex
	out    io.Writer
	level  Level
	fields map[string]any
}

// New builds a logger writing to out.
func New(out io.Writer, level Level) *Logger {
	return &Logger{mu: &sync.Mutex{}, out: out, level: level, fields: map[string]any{}}
}

// With returns a child logger carrying additional fields. Key/value pairs are
// copied so concurrent children never share a map.
func (l *Logger) With(pairs ...any) *Logger {
	child := &Logger{mu: l.mu, out: l.out, level: l.level, fields: make(map[string]any, len(l.fields)+len(pairs)/2)}
	for key, value := range l.fields {
		child.fields[key] = value
	}
	mergePairs(child.fields, pairs)
	return child
}

// Debug emits a debug record.
func (l *Logger) Debug(msg string, pairs ...any) { l.log(LevelDebug, msg, pairs) }

// Info emits an informational record.
func (l *Logger) Info(msg string, pairs ...any) { l.log(LevelInfo, msg, pairs) }

// Warn emits a warning record.
func (l *Logger) Warn(msg string, pairs ...any) { l.log(LevelWarn, msg, pairs) }

// Error emits an error record.
func (l *Logger) Error(msg string, pairs ...any) { l.log(LevelError, msg, pairs) }

func (l *Logger) log(level Level, msg string, pairs []any) {
	if level < l.level {
		return
	}
	record := make(map[string]any, len(l.fields)+len(pairs)/2+3)
	for key, value := range l.fields {
		record[key] = value
	}
	mergePairs(record, pairs)
	record["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["level"] = level.String()
	record["msg"] = msg

	encoded, err := json.Marshal(record)
	if err != nil {
		encoded = []byte(fmt.Sprintf(`{"level":%q,"msg":%q,"log_error":%q}`, level.String(), msg, err.Error()))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(append(encoded, '\n'))
}

func mergePairs(dst map[string]any, pairs []any) {
	for i := 0; i+1 < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			key = fmt.Sprint(pairs[i])
		}
		dst[key] = normalise(pairs[i+1])
	}
	if len(pairs)%2 == 1 {
		dst["dangling_field"] = fmt.Sprint(pairs[len(pairs)-1])
	}
}

func normalise(value any) any {
	switch typed := value.(type) {
	case error:
		if typed == nil {
			return nil
		}
		return typed.Error()
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	case time.Duration:
		return typed.String()
	case map[string]string:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+"="+typed[key])
		}
		return strings.Join(parts, ",")
	default:
		return value
	}
}
