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

func Load() (Credentials, error) {
	var credentials Credentials
	var err error
	credentials.Token, credentials.Generated, err = ResolveToken()
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
func ResolveToken() (token string, generated bool, err error) {
	if token = strings.TrimSpace(os.Getenv("LYCAON_API_TOKEN")); token != "" {
		return token, false, nil
	}
	token, err = api.ResolveAPIToken()
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}
