package httpapi

import (
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

type consoleHandler struct {
	files http.FileSystem
}

func newConsoleHandler(directory string) consoleHandler {
	if directory == "" {
		return consoleHandler{}
	}
	return consoleHandler{files: http.Dir(directory)}
}

func (handler consoleHandler) serve(c *gin.Context) bool {
	if handler.files == nil || (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
		return false
	}
	requestPath := c.Request.URL.Path
	if reservedConsolePath(requestPath) {
		return false
	}
	name := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	if handler.serveFile(c, name, name == "index.html") {
		return true
	}
	// Vite client routes have no file extension. Missing assets must remain a
	// real 404 instead of receiving HTML with a successful status.
	if path.Ext(name) != "" {
		return false
	}
	return handler.serveFile(c, "index.html", true)
}

func (handler consoleHandler) serveFile(c *gin.Context, name string, entrypoint bool) bool {
	file, err := handler.files.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if entrypoint {
		c.Header("Cache-Control", "no-cache")
	} else if strings.HasPrefix(name, "assets/") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	c.Abort()
	return true
}

func reservedConsolePath(requestPath string) bool {
	for _, prefix := range []string{"/api", "/agent-api", "/agent-install", "/healthz", "/readyz"} {
		if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
			return true
		}
	}
	return false
}
