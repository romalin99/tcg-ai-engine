package router

import (
	"context"
	"net"
	"os"
	"strconv"

	swaggo "github.com/gofiber/contrib/v3/swaggo"
	"github.com/gofiber/fiber/v3"

	docs "tcg-ai-engine/docs"
	"tcg-ai-engine/internal/config"
	"tcg-ai-engine/pkg/logs"
)

// InitSwagger registers the Swagger UI routes on the given Fiber app.
// It resolves the host IP (overridable via the SWAGGER_HOST environment variable),
// populates the SwaggerInfo metadata, and mounts the UI at /swagger/*.
func InitSwagger(app *fiber.App, c *config.Config) {
	host := getLocalIP()

	if envHost := os.Getenv("SWAGGER_HOST"); envHost != "" {
		host = envHost
	}

	if host == "" {
		host = getOutboundIP()
	}

	docs.SwaggerInfo.Host = net.JoinHostPort(host, strconv.Itoa(c.Server.Port()))
	docs.SwaggerInfo.BasePath = "/"
	docs.SwaggerInfo.Title = "REST API Document For TCG-AI-ENGINE"
	docs.SwaggerInfo.Description = "电商风控规则引擎（grule）API"
	docs.SwaggerInfo.Version = "1.0"
	docs.SwaggerInfo.Schemes = []string{"http"} // add "https" in production

	// Mount the Swagger UI; the spec is served automatically from the docs package.
	app.Get("/swagger/*", swaggo.New(swaggo.Config{
		Title:                    "TCG-AI-ENGINE API Documentation",
		DeepLinking:              true,   // enable deep-linking to individual operations
		DocExpansion:             "list", // "list" | "full" | "none"
		DefaultModelsExpandDepth: -1,     // -1 = collapse models, 1 = expand one level
		DefaultModelExpandDepth:  1,
		DisplayOperationId:       true,   // show operationId (useful for debugging)
		DisplayRequestDuration:   true,   // show per-request latency (useful in development)
		PersistAuthorization:     false,  // do not persist Authorization header (recommended for production)
		ValidatorUrl:             "none", // Close the validator badge in the bottom right corner.
		CustomStyle: `
		.opblock-summary-operation-id {
		   word-break: keep-all !important;
		}
		`,
	}))
}

// getOutboundIP returns the preferred outbound IP address of the host by
// opening a UDP connection to a well-known external address (8.8.8.8:80).
// No data is actually sent; the OS merely selects the appropriate local interface.
// Falls back to getLocalIP if the UDP dial fails.
func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return getLocalIP()
	}

	defer func() {
		if err := conn.Close(); err != nil {
			logs.Warn(context.Background(), "conn.Close failed: %v", err)
		}
	}()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

// getLocalIP returns the first non-loopback IPv4 address found on the host's
// network interfaces. It returns "localhost" if no suitable address is found
// or if interface enumeration fails.
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}

	return "localhost"
}
