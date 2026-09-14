package logger

import (
	"log/slog"
	"testing"
	"time"
)

func TestRingBufferCapacityAndOverflow(t *testing.T) {
	ring := NewRingBuffer(5)
	for i := 1; i <= 7; i++ {
		ring.Add(LogEntry{
			Timestamp: time.Now(),
			Level:     "INFO",
			Message:   string(rune('A' - 1 + i)),
		})
	}

	all := ring.GetAll()
	if len(all) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(all))
	}

	// Oldest 2 ('A' and 'B') should be overwritten, remaining should be C, D, E, F, G
	expected := []string{"C", "D", "E", "F", "G"}
	for i, entry := range all {
		if entry.Message != expected[i] {
			t.Errorf("entry %d: expected %s, got %s", i, expected[i], entry.Message)
		}
	}
}

func TestSensitiveFiltering(t *testing.T) {
	tempDir := t.TempDir()
	_, ring, closer, err := InitLogger("INFO", tempDir)
	if err != nil {
		t.Fatalf("InitLogger failed: %v", err)
	}
	defer closer.Close()

	slog.Info("User logged in with secret token",
		"password", "super_secret_123",
		"token", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.t-IDcSemACt8x4iTMCda8Yhe3iZaWbvV5XKSTbuAn0M",
		"normal_field", "safe_value",
	)

	entries := ring.GetAll()
	if len(entries) == 0 {
		t.Fatalf("expected log entry in ring buffer")
	}

	last := entries[len(entries)-1]
	if last.Attrs["password"] != "[REDACTED]" {
		t.Errorf("expected password to be redacted, got %v", last.Attrs["password"])
	}
	if last.Attrs["token"] != "[REDACTED]" {
		t.Errorf("expected token to be redacted, got %v", last.Attrs["token"])
	}
	if last.Attrs["normal_field"] != "safe_value" {
		t.Errorf("expected normal_field to be safe_value, got %v", last.Attrs["normal_field"])
	}
}

func TestSubscriber(t *testing.T) {
	ring := NewRingBuffer(10)
	ch := ring.Subscribe(10)

	ring.Add(LogEntry{
		Level:   "INFO",
		Message: "hello sub",
	})

	select {
	case msg := <-ch:
		if msg.Message != "hello sub" {
			t.Errorf("expected 'hello sub', got %s", msg.Message)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for subscriber message")
	}

	ring.Unsubscribe(ch)
}
