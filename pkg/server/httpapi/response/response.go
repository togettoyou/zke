package response

import (
	"github.com/gin-gonic/gin"
)

const SuccessMessage = "Success"

type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

type ErrorData struct {
	ErrorCode string `json:"error_code"`
	RequestID string `json:"request_id"`
}

type Error struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    ErrorData `json:"data"`
}

func WriteSuccess(c *gin.Context, status int, data any) {
	c.JSON(status, Envelope{
		Code:    status,
		Message: SuccessMessage,
		Data:    data,
	})
}

func WriteError(
	c *gin.Context,
	status int,
	code string,
	message string,
	requestID string,
) {
	c.JSON(status, Error{
		Code:    status,
		Message: message,
		Data: ErrorData{
			ErrorCode: code,
			RequestID: requestID,
		},
	})
}
