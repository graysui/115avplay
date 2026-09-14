package admin

import (
	"net/http"
	"time"

	"mediavault/internal/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow same-origin or local connections
		return true
	},
}

type sanitizedLogEntry struct {
	Timestamp string                 `json:"timestamp"`
	Level     string                 `json:"level"`
	Message   string                 `json:"message"`
	Attrs     map[string]interface{} `json:"attrs,omitempty"`
}

func sanitizeEntry(e logger.LogEntry) sanitizedLogEntry {
	msg := logger.ScrubString(e.Message)
	attrs := make(map[string]interface{})

	for k, v := range e.Attrs {
		if logger.IsSensitiveKey(k) {
			attrs[k] = "[REDACTED]"
			continue
		}
		if s, ok := v.(string); ok {
			attrs[k] = logger.ScrubString(s)
		} else {
			attrs[k] = v
		}
	}

	return sanitizedLogEntry{
		Timestamp: e.Timestamp.Format(time.RFC3339),
		Level:     e.Level,
		Message:   msg,
		Attrs:     attrs,
	}
}

// LogsWebSocketHandler handles GET /api/v1/ws/logs.
func LogsWebSocketHandler(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		_ = c.Error(err)
		return
	}
	defer conn.Close()

	// 1. Send recent backlog from ring buffer
	if logger.GlobalRingBuffer != nil {
		backlog := logger.GlobalRingBuffer.GetAll()
		for _, entry := range backlog {
			sanitized := sanitizeEntry(entry)
			if err := conn.WriteJSON(sanitized); err != nil {
				return
			}
		}
	}

	// 2. Subscribe to new entries
	ch := logger.GlobalRingBuffer.Subscribe(200)
	defer logger.GlobalRingBuffer.Unsubscribe(ch)

	// Channel to signal client closed
	clientClosed := make(chan struct{})

	// Read pump to catch close messages
	go func() {
		defer close(clientClosed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-clientClosed:
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			sanitized := sanitizeEntry(entry)
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteJSON(sanitized); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, []byte("keepalive")); err != nil {
				return
			}
		case <-c.Request.Context().Done():
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutdown"))
			return
		}
	}
}
