package toolexecution

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/url"
	"slices"
	"strconv"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// secret_use declares reviewed recipients for setup and later HTTP requests.
func parseSecretUse(contract toolcontract.Contract, args map[string]any) (*secretUseDeclaration, error) {
	raw, present := args["secret_use"]
	if !present {
		return nil, nil
	}
	if !contract.SecretReferenceSurface.ProcessArguments() {
		return nil, fmt.Errorf("secret_use is only available on command and terminal tools")
	}
	request, ok := raw.(map[string]any)
	if !ok || len(request) != 1 {
		return nil, fmt.Errorf("secret_use requires a services array")
	}
	services, ok := request["services"].([]any)
	if !ok || len(services) == 0 || len(services) > 8 {
		return nil, fmt.Errorf("secret_use.services requires 1 to 8 HTTP origins")
	}
	if !secretcap.ReferenceUseInSlots(args, nil).Complete {
		return nil, toolrejection.RejectInvalidArguments("SECRET_USE_WITHOUT_REFERENCE", map[string]any{"field": "secret_use"})
	}
	var recipients []secretmatch.Recipient
	var connectPorts []uint16
	loopback := true
	for _, service := range services {
		address, ok := service.(string)
		if !ok || len(address) > 1024 || secretmatch.ContainsReferenceToken(address) {
			return nil, fmt.Errorf("secret_use service must be an HTTP origin without credentials")
		}
		declared, parseErr := url.Parse(address)
		if parseErr != nil || declared.Fragment != "" || declared.ForceQuery {
			return nil, fmt.Errorf("secret_use service must contain only scheme, host, and port")
		}
		target, err := outboundhttp.NormalizeURL(address)
		if err != nil || target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
			(target.Path != "" && target.Path != "/") {
			return nil, fmt.Errorf("secret_use service must contain only scheme, host, and port")
		}
		if port := target.Port(); port != "" {
			parsed, err := strconv.ParseUint(port, 10, 16)
			if err != nil || parsed == 0 {
				return nil, fmt.Errorf("secret_use service port must be between 1 and 65535")
			}
		}
		id, label := secretmatch.HTTPDestination(target)
		recipients = append(recipients, secretmatch.Recipient{
			ID: id, Label: label, Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService,
		})
		if !egress.SyntacticLoopback(target.Hostname()) {
			loopback = false
			continue
		}
		port := outboundhttp.DefaultPort(target.Scheme)
		if target.Port() != "" {
			explicit, _ := strconv.ParseUint(target.Port(), 10, 16)
			port = uint16(explicit)
		}
		connectPorts = append(connectPorts, port)
	}
	canonical, err := secretmatch.CanonicalRecipients(recipients)
	if err != nil {
		return nil, err
	}
	slices.Sort(connectPorts)
	return &secretUseDeclaration{recipients: canonical, connectPorts: slices.Compact(connectPorts), loopback: loopback}, nil
}

type secretUseContextKey struct{}

type secretUseDeclaration struct {
	recipients   []secretmatch.Recipient
	connectPorts []uint16
	// loopback is true when every declared service is reached over loopback.
	loopback bool
}

func withSecretUse(ctx context.Context, declaration *secretUseDeclaration) context.Context {
	return context.WithValue(ctx, secretUseContextKey{}, declaration)
}

func secretUseFrom(ctx context.Context) []secretmatch.Recipient {
	declaration, _ := ctx.Value(secretUseContextKey{}).(*secretUseDeclaration)
	if declaration == nil {
		return nil
	}
	return declaration.recipients
}

// argvSecretRecipientsLocal reports whether a process handoff stays on this
// machine: the process is the chat's own, and every service it was declared to
// pass the value to is reached over loopback.
func argvSecretRecipientsLocal(ctx context.Context) bool {
	declaration, _ := ctx.Value(secretUseContextKey{}).(*secretUseDeclaration)
	return declaration == nil || declaration.loopback
}

func secretUseConnectPorts(ctx context.Context) []uint16 {
	declaration, _ := ctx.Value(secretUseContextKey{}).(*secretUseDeclaration)
	if declaration == nil {
		return nil
	}
	return declaration.connectPorts
}
