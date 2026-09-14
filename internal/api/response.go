package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// APIError represents the standardized error body for admin and general APIs.
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// ErrorResponse wraps the APIError under "error" field according to design §10.1.
type ErrorResponse struct {
	Error APIError `json:"error"`
}

// SuccessResponse wraps successful data under "data" field according to design §10.1.
type SuccessResponse struct {
	Data interface{} `json:"data"`
}

// SendError formats and sends a standardized JSON error response.
func SendError(c *gin.Context, httpStatus int, code, message string) {
	reqID, _ := c.Get("request_id")
	reqIDStr, _ := reqID.(string)

	c.Header("Content-Type", "application/json; charset=utf-8")
	c.AbortWithStatusJSON(httpStatus, ErrorResponse{
		Error: APIError{
			Code:      code,
			Message:   message,
			RequestID: reqIDStr,
		},
	})
}

// SendSuccess formats and sends a standardized JSON success response.
func SendSuccess(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, SuccessResponse{
		Data: data,
	})
}

// SendStatus202 sends a 202 Accepted response for background jobs.
func SendAccepted(c *gin.Context, jobID, state string) {
	c.JSON(http.StatusAccepted, gin.H{
		"job_id": jobID,
		"state":  state,
	})
}
