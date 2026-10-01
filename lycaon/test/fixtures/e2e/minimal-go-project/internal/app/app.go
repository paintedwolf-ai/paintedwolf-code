package app

import (
	"net/http"

	"github.com/painted-wolf/e2e-fixture/internal/config"
)

// Run loads configuration before constructing the HTTP server.
func Run() error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.ListenAddr, Handler: Routes()}
	return server.ListenAndServe()
}
