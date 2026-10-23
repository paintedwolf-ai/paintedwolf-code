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

// Transfer is one completed exchange's identity: the bytes the server sent
// and the response body handed downstream, after any requested decompression.
type Transfer struct {
	Status         int
	FinalURL       string
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
	stdout   io.Writer
	stderr   io.Writer
	dests    *mutation.Destinations
	started  time.Time
	sent     bool
	transfer Transfer
}

func newInvocation(ctx context.Context, p Prepared, tc tools.ToolContext, streams commandprogram.Streams) *invocation {
	inv := &invocation{ctx: ctx, owners: p.owners, tc: tc, program: p.program, request: p.request, opts: p.options, stdout: streams.Output, stderr: streams.Error}
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
	limits := httpaction.ExchangeOptions{Jar: jar, MaxBodyBytes: inv.owners.limits["stream_bytes"], ConnectTimeout: inv.opts.connectTimeout, WireHeaders: true}
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
	inv.transfer = Transfer{Status: resp.Status, FinalURL: ex.FinalURL, ReceivedBytes: resp.Bytes, ReceivedSHA256: resp.SHA256}
	// curl writes every followed hop's header block, then the final one.
	// Cleartext responses keep received order and spelling; TLS responses
	// present the owner's normalized order.
	var blocks strings.Builder
	for _, hop := range ex.Redirects {
		blocks.WriteString(presentedBlock(hop.StatusLine, hop.Headers, hop.Wire))
	}
	blocks.WriteString(presentedBlock(resp.StatusLine, resp.Headers, resp.Wire))
	headers := blocks.String()
	if inv.opts.dumpHeader == "-" {
		if _, err := io.WriteString(inv.stdout, headers); err != nil {
			return err
		}
	} else if headerFile != nil {
		if err := inv.dests.Publish(inv.ctx, *headerFile, []byte(headers)); err != nil {
			return err
		}
	}
	if inv.opts.fail && resp.Status >= 400 {
		return &curlFailure{code: 22, message: fmt.Sprintf("The requested URL returned error: %d", resp.Status), certainty: toolcontract.EffectCompleted}
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
	return inv.writeOut(resp.Status, resp.Bytes, ex.FinalURL)
}

// writeOut renders -w after the transfer; a failed transfer reports 000.
func (inv *invocation) writeOut(status int, size int64, finalURL string) error {
	if !inv.opts.hasWriteOut {
		return nil
	}
	var b strings.Builder
	for _, token := range inv.opts.writeOut {
		switch token.variable {
		case "":
			b.WriteString(token.literal)
		case "http_code":
			fmt.Fprintf(&b, "%03d", status)
		case "size_download":
			fmt.Fprintf(&b, "%d", size)
		case "url_effective":
			b.WriteString(finalURL)
		case "time_total":
			fmt.Fprintf(&b, "%.6f", time.Since(inv.started).Seconds())
		}
	}
	_, err := io.WriteString(inv.stdout, b.String())
	return err
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
