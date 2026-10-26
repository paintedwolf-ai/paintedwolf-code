// Package curl is the command frontend for the catalogued curl forms. Every
// request goes through the http_request owner's complete lifecycle (screening,
// recipient approval, secret and cookie handling, loopback authority and the
// per-hop egress gate) via httpaction.Perform; there is no second client.
// Response bytes reach stdout unchanged; files named by
// -o, -D and -c publish through the pinned exact-bytes door, and a request
// that may have reached the server without a response settles as an unknown
// effect that program recovery never retries.
package curl

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
)

// Owners holds the shared operations behind curl. One value serves many
// programs; per-invocation state lives in Prepared.
type Owners struct {
	HTTP      httpaction.Deps
	Mutations *mutation.Owners

	spec      toolcontract.CommandSpecification
	operation string
	limits    map[string]int64
}

func NewOwners(http httpaction.Deps, mutations *mutation.Owners) (*Owners, error) {
	if http.Boundary == nil || mutations == nil {
		return nil, errors.New("curl requires the HTTP and publication owners")
	}
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return nil, err
	}
	o := &Owners{HTTP: http, Mutations: mutations, limits: map[string]int64{}}
	for _, command := range catalog.Commands {
		if command.Name == "curl" {
			o.spec = command
		}
	}
	for _, disposition := range catalog.Tools {
		for _, form := range disposition.CommandForms {
			if form == "curl" {
				o.operation = disposition.Tool
			}
		}
	}
	for _, limit := range catalog.Language.Limits {
		o.limits[limit.ID] = limit.Maximum
	}
	if o.spec.Name == "" || o.operation == "" || o.limits["stream_bytes"] < 1 {
		return nil, errors.New("curl is missing from the language catalog")
	}
	return o, nil
}

// Operation is the catalog operation identity that owns curl.
func (o *Owners) Operation() string { return o.operation }

// Prepared is one bound curl invocation; InvokeOwner matches
// toolexecution.PreparedNativeEffect, so streams arrive after admission.
type Prepared struct {
	Mode          string
	Operation     string
	Argv          commandprogram.BoundArgv
	Review        commandprogram.ArgvReview
	LoopbackPorts []uint16
	owners        *Owners
	program       mutation.Program
	request       commandprogram.OperationRequest
	options       options
	transfer      *Transfer
}

// Transfer reports the completed exchange's received and emitted identities
// after InvokeOwner returns; it is zero when no response arrived.
func (p Prepared) Transfer() Transfer {
	if p.transfer == nil {
		return Transfer{}
	}
	return *p.transfer
}

// Prepare binds argv through the catalog grammar and validates every form
// before anything is read, sent or written.
func (o *Owners) Prepare(program mutation.Program, request commandprogram.OperationRequest) (Prepared, error) {
	if err := request.Validate(); err != nil {
		return Prepared{}, err
	}
	if request.Argv[0].Value != "curl" || program.Paths == nil {
		return Prepared{}, fmt.Errorf("%s is not a curl invocation", request.Argv[0].Value)
	}
	if request.OperationID != "curl" && request.OperationID != o.operation {
		return Prepared{}, fmt.Errorf("curl belongs to operation %s, not %s", o.operation, request.OperationID)
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(o.spec, request.Argv)
	if err != nil {
		var argvErr *commandprogram.ArgvError
		if errors.As(err, &argvErr) {
			return Prepared{}, errors.Join(err, &toolrejection.ToolReject{Code: argvErr.Code, Data: map[string]any{
				"command": "curl", "word": argvErr.Word, "expected": argvErr.Expected, "expected_form": o.spec.Argv}})
		}
		return Prepared{}, err
	}
	opts, err := parseOptions(bound)
	if err != nil {
		return Prepared{}, err
	}
	mode := "request"
	for _, destination := range []string{opts.output, opts.dumpHeader, opts.cookieJar} {
		if destination != "" && destination != "-" && destination != "/dev/null" {
			mode = "-o--D--c-to-file"
		}
	}
	review := bound.Review(o.operation)
	review.PathTargets = curlPathTargets(opts)
	return Prepared{Mode: mode, Operation: o.operation, Argv: bound, Review: review, LoopbackPorts: loopbackPorts(opts.url), owners: o, program: program, request: request, options: opts, transfer: &Transfer{}}, nil
}

func curlPathTargets(opts options) []commandprogram.PathTarget {
	targets := []commandprogram.PathTarget{}
	add := func(path string, write bool) {
		if path != "" && path != "-" && path != "/dev/null" {
			targets = append(targets, commandprogram.PathTarget{Path: path, Write: write})
		}
	}
	for _, path := range []string{opts.output, opts.dumpHeader, opts.cookieJar} {
		add(path, true)
	}
	for _, path := range opts.cookies {
		if !strings.Contains(path, "=") {
			add(path, false)
		}
	}
	for _, part := range opts.data {
		if part.kind != "raw" && strings.HasPrefix(part.value, "@") {
			add(strings.TrimPrefix(part.value, "@"), false)
		}
	}
	for _, form := range opts.forms {
		_, value, ok := strings.Cut(form, "=")
		if ok && strings.HasPrefix(value, "@") {
			path, _, _ := strings.Cut(strings.TrimPrefix(value, "@"), ";")
			add(path, false)
		}
	}
	return targets
}

func loopbackPorts(raw string) []uint16 {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	addr, addrErr := netip.ParseAddr(host)
	if host != "localhost" && (addrErr != nil || !addr.IsLoopback()) {
		return nil
	}
	port := 0
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil
		}
	} else if parsed.Scheme == "https" {
		port = 443
	} else if parsed.Scheme == "http" {
		port = 80
	}
	if port == 0 {
		return nil
	}
	return []uint16{uint16(port)}
}

// InvokeOwner performs the request once and settles curl's exit status.
func (p Prepared) InvokeOwner(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	inv := newInvocation(ctx, p, tc, streams)
	err := inv.run()
	if p.transfer != nil {
		*p.transfer = inv.transfer
	}
	if streams.Input != nil {
		_ = streams.Input.Close()
	}
	return inv.settle(err)
}
