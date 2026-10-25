package deviceidentity

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"os"
	"strings"
)

type Credentials struct {
	Token     string
	Generated bool
	Host      hostidentity.Identity
}

// Load resolves the device's API bearer and host identity; delivered is the
// token the desktop sent over the startup pipe, when it sent one.
func Load(delivered string) (Credentials, error) {
	var credentials Credentials
	var err error
	credentials.Token, credentials.Generated, err = ResolveToken(delivered)
	if err != nil {
		return Credentials{}, fmt.Errorf("api token: %w", err)
	}
	directory, err := configdir.UserConfigDir()
	if err != nil {
		return Credentials{}, fmt.Errorf("config dir: %w", err)
	}
	credentials.Host, err = hostidentity.LoadOrCreate(directory)
	if err != nil {
		return Credentials{}, fmt.Errorf("host identity: %w", err)
	}
	return credentials, nil
}
// ResolveToken prefers the delivered token, then LYCAON_API_TOKEN for a
// standalone engine, then a generated one.
func ResolveToken(delivered string) (token string, generated bool, err error) {
	if token = strings.TrimSpace(delivered); token != "" {
		return token, false, nil
	}
	if token = strings.TrimSpace(os.Getenv("LYCAON_API_TOKEN")); token != "" {
		return token, false, nil
	}
	token, err = api.ResolveAPIToken()
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}
