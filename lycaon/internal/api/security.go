package api

import (
	"os"

	"github.com/go-chi/cors"
	"github.com/lycaon/lycaon/internal/configdir"
)

// productionCORSOrigins lists shipped desktop UI origins.
var productionCORSOrigins = []string{
	"tauri://localhost",
	"http://tauri.localhost",
	"https://tauri.localhost",
}

// devServerCORSOrigins apply only to development builds.
var devServerCORSOrigins = []string{
	"http://localhost:1420",
	"http://127.0.0.1:1420",
}

func corsAllowedOrigins() []string {
	origins := append([]string(nil), productionCORSOrigins...)
	if configdir.IsDevelopmentBuild() {
		origins = append(origins, devServerCORSOrigins...)
	}
	return origins
}

func devCORSEnabled() bool {
	if os.Getenv("LYCAON_ENV") == "production" {
		return false
	}
	if os.Getenv("LYCAON_DEV_CORS") == "1" && configdir.IsDevelopmentChannel() {
		return true
	}
	return false
}

func corsOptions() cors.Options {
	origins := corsAllowedOrigins()
	if devCORSEnabled() {
		origins = []string{"*"}
	}
	return cors.Options{
		AllowedOrigins: origins,
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "If-None-Match", "X-Request-ID"},
		ExposedHeaders: []string{"Content-Disposition", "X-Export-Truncated", "ETag", "Retry-After", "Location"},
		// API authentication uses explicit headers, not ambient cookies.
		AllowCredentials: false,
		// The preflight cache is limited to 600 seconds per URL.
		MaxAge: 600,
	}
}
