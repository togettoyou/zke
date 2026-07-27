package httpapi

import (
	"github.com/gin-gonic/gin"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
)

func writeSuccess(c *gin.Context, status int, data any) {
	apiresponse.WriteSuccess(c, status, data)
}

func writeError(c *gin.Context, status int, code string, message string) {
	apiresponse.WriteError(c, status, code, message, httpmiddleware.RequestID(c))
}
