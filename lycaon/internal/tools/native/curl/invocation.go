package curl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
)

// Transfer is one completed exchange's identity: the request body as sent,
// the bytes the server sent and the response body handed downstream, after
// any requested decompression.
type Transfer struct {
	Status         int
	FinalURL       string
	SentBytes      int64
	SentSHA256     string
	ReceivedBytes  int64
	ReceivedSHA256 string
	EmittedBytes   int64
	EmittedSHA256  string
}

// invocation is one curl run: its options, streams, captured destinations
// and the outcome reported as curl's exit status.
type invocation struct {
	ctx      context.Context
	owners   *Owners
	tc       tools.ToolContext
	program  mutation.Program
	request  commandprogram.OperationRequest
	opts     options
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	dests    *mutation.Destinations
	started  time.Time
	sent     bool
	exchange *httpaction.Exchange
	headers  string
	transfer Transfer
}

func newInvocation(ctx context.Context, p Prepared, tc tools.ToolContext, streams commandprogram.Streams) *invocation {
	inv := &invocation{ctx: ctx, owners: p.owners, tc: tc, program: p.program, request: p.request, opts: p.options, stdin: streams.Input, stdout: streams.Output, stderr: streams.Error}
	if inv.stdin == nil {
		inv.stdin = strings.NewReader("")
	}
	if inv.stdout == nil {
		inv.stdout = io.Discard
	}
	if inv.stderr == nil {
		inv.stderr = io.Discard
	}
	inv.dests = p.owners.Mutations.Destinations(p.program, tc, p.request.Invocation.Identity.ExecutionID)
	return inv
}

// capture pins an optional destination operand before the request is sent;
// "-" for -D means stdout.
func (inv *invocation) capture(operand string) (*mutation.Destination, error) {
	if operand == "" || operand == "-" {
		return nil, nil
	}
	dest, err := inv.dests.Capture(inv.ctx, operand)
	if err != nil {
		return nil, err
	}
	return &dest, nil
}

func (inv *invocation) run() error {
	output, err := inv.capture(inv.opts.output)
	if err != nil {
		return err
	}
	headerFile, err := inv.capture(inv.opts.dumpHeader)
	if err != nil {
		return err
	}
	jarFile, err := inv.capture(inv.opts.cookieJar)
	if err != nil {
		return err
	}
	jar, cookieHeader, err := inv.cookies()
	if err != nil {
		return err
	}
	args, err := inv.requestArgs(cookieHeader)
	if err != nil {
		return err
	}
	material, err := inv.clientTLS()
	if err != nil {
		return err
	}
	limits := httpaction.ExchangeOptions{Jar: jar, MaxBodyBytes: inv.owners.limits["stream_bytes"], ConnectTimeout: inv.opts.connectTimeout, WireHeaders: true, TLS: material, Dial: inv.opts.dial}
	if inv.opts.location {
		// curl follows up to 50 redirects; the owner's ceiling is lower.
		limits.RedirectLimit = outboundhttp.MaxRedirectLimit
	}
	inv.started = time.Now()
	inv.sent = true
	ex, err := httpaction.Perform(inv.ctx, inv.owners.HTTP, args, inv.tc, limits)
	if err != nil {
		return err
	}
	resp := ex.Response
	inv.exchange = &ex
	inv.transfer = Transfer{Status: resp.Status, FinalURL: ex.FinalURL, SentBytes: ex.SentBodyBytes, SentSHA256: ex.SentBodySHA256, ReceivedBytes: resp.Bytes, ReceivedSHA256: resp.SHA256}
	// curl writes every followed hop's header block, then the final one.
	// Cleartext responses keep received order and spelling; TLS responses
	// present the owner's normalized order.
	var blocks strings.Builder
	for _, hop := range ex.Redirects {
		blocks.WriteString(presentedBlock(hop.StatusLine, hop.Headers, hop.Wire))
	}
	blocks.WriteString(presentedBlock(resp.StatusLine, resp.Headers, resp.Wire))
	headers := blocks.String()
	inv.headers = headers
	if inv.opts.verbose {
		if _, err := io.WriteString(inv.stderr, verbose(ex)); err != nil {
			return err
		}
	}
	if inv.opts.dumpHeader == "-" {
		if _, err := io.WriteString(inv.stdout, headers); err != nil {
			return err
		}
	} else if headerFile != nil {
		if err := inv.dests.Publish(inv.ctx, *headerFile, []byte(headers)); err != nil {
			return err
		}
	}
	failed := inv.opts.fail != failNever && resp.Status >= 400
	if failed && inv.opts.fail == failQuiet {
		return httpFailure(resp.Status)
	}
	received := resp.Body
	if inv.opts.compressed {
		if received, err = decodeContent(resp.Headers, received, inv.owners.limits["stream_bytes"]); err != nil {
			return err
		}
	}
	var body bytes.Buffer
	if inv.opts.include || inv.opts.head {
		body.WriteString(headers)
	}
	body.Write(received)
	sum := sha256.Sum256(received)
	inv.transfer.EmittedBytes, inv.transfer.EmittedSHA256 = int64(len(received)), hex.EncodeToString(sum[:])
	if output != nil {
		if err := inv.dests.Publish(inv.ctx, *output, body.Bytes()); err != nil {
			return err
		}
	} else if _, err := inv.stdout.Write(body.Bytes()); err != nil {
		return err
	}
	if jarFile != nil {
		if err := inv.dests.Publish(inv.ctx, *jarFile, jar.netscape()); err != nil {
			return err
		}
	}
	if failed {
		return httpFailure(resp.Status)
	}
	return nil
}

// httpFailure is --fail's exit for an HTTP error status; the exchange
// completed.
func httpFailure(status int) error {
	return &curlFailure{code: 22, message: fmt.Sprintf("The requested URL returned error: %d", status), certainty: toolcontract.EffectCompleted}
}

func presentedBlock(statusLine string, headers []outboundhttp.Header, wire *outboundhttp.WireHeaders) string {
	if wire != nil {
		return wire.Raw
	}
	return headerBlock(statusLine, headers)
}

// headerBlock is the received status line and headers as CRLF lines, ending
// with the empty line, as curl writes for -i, -I and -D.
func headerBlock(statusLine string, headers []outboundhttp.Header) string {
	var b strings.Builder
	b.WriteString(statusLine + "\r\n")
	for _, h := range headers {
		b.WriteString(h.Name + ": " + h.Value + "\r\n")
	}
	b.WriteString("\r\n")
	return b.String()
}

var _ http.CookieJar = (*cookieJar)(nil)
