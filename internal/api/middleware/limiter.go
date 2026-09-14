package middleware

import (
	"net/http"
	"sync/atomic"

	"mediavault/internal/api"

	"github.com/gin-gonic/gin"
)

// BoundedQueueLimiter limits maximum concurrent and in-flight queued requests.
type BoundedQueueLimiter struct {
	maxInflight int64
	current     int64
}

// NewBoundedQueueLimiter creates a limiter with max in-flight queue limit.
func NewBoundedQueueLimiter(maxInflight int64) *BoundedQueueLimiter {
	if maxInflight <= 0 {
		maxInflight = 1000
	}
	return &BoundedQueueLimiter{
		maxInflight: maxInflight,
	}
}

// Handler returns the Gin middleware.
func (l *BoundedQueueLimiter) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Increment in-flight counter
		current := atomic.AddInt64(&l.current, 1)
		defer atomic.AddInt64(&l.current, -1)

		if current > l.maxInflight {
			api.SendError(c, http.StatusTooManyRequests, "queue_full", "server queue limit reached")
			return
		}

		c.Next()
	}
}

// Current returns current in-flight requests count.
func (l *BoundedQueueLimiter) Current() int64 {
	return atomic.LoadInt64(&l.current)
}
