package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

type ReadinessCheck func(context.Context) error

type Dependencies struct {
	ReadinessCheck ReadinessCheck
	AuthService    *auth.Service
}

type Config struct {
	Authentication AuthenticationConfig
}

type handlers struct {
	health         *healthHandler
	auth           *authHandler
	authMiddleware *httpmiddleware.Authentication
}

var configureGinMode sync.Once

func New(
	logger *slog.Logger,
	dependencies Dependencies,
	config Config,
) http.Handler {
	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})

	router := gin.New()
	router.Use(
		httpmiddleware.Recovery(logger),
		httpmiddleware.RequestLogger(logger),
		httpmiddleware.CrossOriginProtection(),
	)

	routeHandlers := handlers{
		health: newHealthHandler(logger, dependencies.ReadinessCheck),
		auth: newAuthHandler(
			logger,
			dependencies.AuthService,
			config.Authentication,
		),
		authMiddleware: httpmiddleware.NewAuthentication(
			logger,
			dependencies.AuthService,
			httpmiddleware.AuthenticationConfig{
				CookieSecure:     config.Authentication.CookieSecure,
				OperationTimeout: config.Authentication.OperationTimeout,
			},
		),
	}
	registerRoutes(router, routeHandlers)
	return router
}
