package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DailyFileWriter rotates log files on a daily basis.
type DailyFileWriter struct {
	mu         sync.Mutex
	logDir     string
	prefix     string
	currentDay string
	file       *os.File
}

// NewDailyFileWriter creates a new file writer with daily rotation.
func NewDailyFileWriter(logDir, prefix string) (*DailyFileWriter, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &DailyFileWriter{
		logDir: logDir,
		prefix: prefix,
	}
	if err := w.rotate(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *DailyFileWriter) rotate() error {
	today := time.Now().UTC().Format("2006-01-02")
	if w.currentDay == today && w.file != nil {
		return nil
	}

	if w.file != nil {
		_ = w.file.Close()
	}

	fileName := fmt.Sprintf("%s-%s.log", w.prefix, today)
	filePath := filepath.Join(w.logDir, fileName)

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", filePath, err)
	}

	w.file = f
	w.currentDay = today
	return nil
}

func (w *DailyFileWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.rotate(); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func (w *DailyFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

// MemoryRingHandler is an slog.Handler that writes to standard slog handler and ring buffer.
type MemoryRingHandler struct {
	inner      slog.Handler
	ringBuffer *RingBuffer
}

func NewMemoryRingHandler(inner slog.Handler, ring *RingBuffer) *MemoryRingHandler {
	return &MemoryRingHandler{
		inner:      inner,
		ringBuffer: ring,
	}
}

func (h *MemoryRingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *MemoryRingHandler) Handle(ctx context.Context, record slog.Record) error {
	// First extract attributes and filter sensitive values
	attrs := make(map[string]interface{})
	record.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if IsSensitiveKey(key) {
			attrs[key] = "[REDACTED]"
		} else {
			attrs[key] = cleanAttrValue(a.Value.Any())
		}
		return true
	})

	cleanMsg := ScrubString(record.Message)

	// Add to ring buffer
	if h.ringBuffer != nil {
		h.ringBuffer.Add(LogEntry{
			Timestamp: record.Time.UTC(),
			Level:     record.Level.String(),
			Message:   cleanMsg,
			Attrs:     attrs,
		})
	}

	// Forward to inner handler
	return h.inner.Handle(ctx, record)
}

func (h *MemoryRingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &MemoryRingHandler{
		inner:      h.inner.WithAttrs(attrs),
		ringBuffer: h.ringBuffer,
	}
}

func (h *MemoryRingHandler) WithGroup(name string) slog.Handler {
	return &MemoryRingHandler{
		inner:      h.inner.WithGroup(name),
		ringBuffer: h.ringBuffer,
	}
}

func cleanAttrValue(v interface{}) interface{} {
	if str, ok := v.(string); ok {
		return ScrubString(str)
	}
	return v
}

// Global logger references
var (
	GlobalRingBuffer = NewRingBuffer(2000)
	GlobalLogger     *slog.Logger
)

// InitLogger initializes global slog logger with ring buffer and optional file logging.
func InitLogger(levelStr string, logDir string) (*slog.Logger, *RingBuffer, io.Closer, error) {
	var level slog.Level
	switch levelStr {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	ring := NewRingBuffer(2000)
	GlobalRingBuffer = ring

	var writers []io.Writer
	writers = append(writers, os.Stdout)

	var closer io.Closer
	if logDir != "" {
		fileWriter, err := NewDailyFileWriter(logDir, "mediavault")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("init daily file writer: %w", err)
		}
		writers = append(writers, fileWriter)
		closer = fileWriter
	}

	multiWriter := io.MultiWriter(writers...)
	baseHandler := slog.NewJSONHandler(multiWriter, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if IsSensitiveKey(a.Key) {
				return slog.String(a.Key, "[REDACTED]")
			}
			if a.Value.Kind() == slog.KindString {
				return slog.String(a.Key, ScrubString(a.Value.String()))
			}
			return a
		},
	})

	ringHandler := NewMemoryRingHandler(baseHandler, ring)
	logger := slog.New(ringHandler)
	slog.SetDefault(logger)
	GlobalLogger = logger

	return logger, ring, closer, nil
}
