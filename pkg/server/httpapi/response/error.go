package response

import (
	"github.com/gin-gonic/gin"
)

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func WriteError(
	c *gin.Context,
	status int,
	code string,
	message string,
	requestID string,
) {
	c.JSON(status, Error{
		Code:      code,
		Message:   message,
		RequestID: requestID,
	})
}
