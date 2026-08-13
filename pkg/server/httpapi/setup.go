package httpapi

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

type setupService interface {
	SetupRequired(context.Context) (bool, error)
	SetupAdministrator(context.Context, auth.AdministratorSetupInput) (auth.User, error)
	Login(context.Context, auth.LoginInput) (auth.LoginResult, error)
}

type setupHandler struct {
	baseHandler
	service       setupService
	auth          *authHandler
	sourceLimiter *sourceLimiter
}

type setupStatusResponse struct {
	Required bool `json:"required"`
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func newSetupHandler(
	logger *slog.Logger,
	service setupService,
	authHandler *authHandler,
	config AuthenticationConfig,
) *setupHandler {
	return &setupHandler{
		baseHandler:   newBaseHandler(logger, nil, config.OperationTimeout),
		service:       service,
		auth:          authHandler,
		sourceLimiter: newSourceLimiter(config.LoginRateLimitWindow, config.MaxAttemptsPerSource),
	}
}

func (handler *setupHandler) status(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	operationContext, cancelOperation := handler.operationContext(c)
	required, err := handler.service.SetupRequired(operationContext)
	cancelOperation()
	if err != nil {
		handler.respondError(c, "check system setup state", err)
		return
	}
	writeSuccess(c, http.StatusOK, setupStatusResponse{Required: required})
}

func (handler *setupHandler) initialize(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if allowed, retry := handler.sourceLimiter.allow(clientAddress(c.Request), time.Now()); !allowed {
		retrySeconds := max(1, int(math.Ceil(retry.Seconds())))
		c.Header("Retry-After", strconv.Itoa(retrySeconds))
		writeError(c, http.StatusTooManyRequests, "too_many_requests", "too many setup attempts")
		return
	}

	var request setupRequest
	if err := decodeJSONRequest(c, &request, maxLoginRequestBytes); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid setup request")
		return
	}
	password := []byte(request.Password)
	request.Password = ""
	defer clear(password)

	requestID := httpmiddleware.RequestID(c)
	operationContext, cancelOperation := handler.operationContext(c)
	_, err := handler.service.SetupAdministrator(operationContext, auth.AdministratorSetupInput{
		Username: request.Username, Password: password, RequestID: requestID,
	})
	if err == nil {
		var result auth.LoginResult
		result, err = handler.service.Login(operationContext, auth.LoginInput{
			Username: request.Username, Password: password,
			RequestID: requestID, Now: time.Now().UTC(),
		})
		if err == nil {
			handler.auth.setAuthenticationCookies(c, result)
			writeSuccess(c, http.StatusCreated, authenticationResponse{
				User: responseUser(result.User), ExpiresAt: responseTime(result.ExpiresAt),
			})
		}
	}
	cancelOperation()
	if err == nil {
		return
	}
	if handler.respondError(c, "initialize global administrator", err,
		errorMapping{auth.ErrSetupAlreadyCompleted, http.StatusConflict, "setup_completed", "system setup is already complete"},
		errorMapping{auth.ErrSetupUsernameUnavailable, http.StatusConflict, "username_unavailable", "username is unavailable"},
		errorMapping{auth.ErrInvalidSetupUsername, http.StatusBadRequest, "invalid_username", "username is invalid"},
		errorMapping{auth.ErrInvalidNewPassword, http.StatusBadRequest, "invalid_password", "password does not satisfy password policy"},
	) {
		return
	}
}
