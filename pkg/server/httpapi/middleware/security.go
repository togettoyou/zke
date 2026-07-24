package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
)

func CrossOriginProtection() gin.HandlerFunc {
	protection := http.NewCrossOriginProtection()
	return func(c *gin.Context) {
		if err := protection.Check(c.Request); err != nil {
			apiresponse.WriteError(
				c,
				http.StatusForbidden,
				"cross_origin_forbidden",
				"cross-origin request forbidden",
				RequestID(c),
			)
			c.Abort()
			return
		}
		c.Next()
	}
}
