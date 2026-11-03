package curl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// curlFailure is an ordinary curl exit: an HTTP or transport outcome the
// program may handle, with the effect certainty the owner's facts establish.
type curlFailure struct {
	code      int
	message   string
	certainty toolcontract.EffectCertainty
	cause     error
}

func (f *curlFailure) Error() string { return fmt.Sprintf("curl: (%d) %s", f.code, f.message) }
func (f *curlFailure) Unwrap() error { return f.cause }

// TransportOutcome classifies a failed exchange as curl's exit status and the
// producer outcome a program child settles with. A request that may have
// reached the server without a response is an unknown effect: it is reported,
// never replayed, and program recovery sends no retry.
func TransportOutcome(err error) (int, toolcontract.ProducerResult) {
	f := classify(err)
	terminal := toolcontract.ProducerFailed
	if errors.Is(err, context.Canceled) {
		terminal = toolcontract.ProducerInterrupted
	}
	return f.code, toolcontract.ProducerResult{Status: f.code, Terminal: terminal, EffectCertainty: f.certainty}
}

// classify maps the owner's exchange facts to curl's exit codes: 6 DNS,
// 7 connect, 18 partial body, 28 timeout, 47 redirects, 52 no reply, 56
// receive failure (including a peer's TLS alert, such as a refused client
// certificate), 60 peer certificate verification, 63 size ceiling.
func classify(err error) *curlFailure {
	var failure *curlFailure
	if errors.As(err, &failure) {
		return failure
	}
	cause := err
	var transport *httpaction.TransportFailure
	if errors.As(err, &transport) {
		cause = transport.Cause
	}
	var exchange *outboundhttp.ExchangeError
	started, received := false, false
	if errors.As(cause, &exchange) {
		started, received = exchange.RequestsStarted > 0, exchange.ResponsesReceived > 0
	}
	var dns *net.DNSError
	var op *net.OpError
	var tlsFault *outboundhttp.TLSFault
	var redirect *outboundhttp.RedirectError
	var oversized *outboundhttp.BodyTooLargeError
	var partial *outboundhttp.TransferError
	dial := errors.As(cause, &op) && op.Op == "dial"
	// crypto/tls reports a peer's alert as the "remote error" operation.
	alert := op != nil && op.Op == "remote error"
	timeout := errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, os.ErrDeadlineExceeded) || isTimeout(cause)
	// After the first byte may have left, the outcome is unknown until a
	// response arrives; the earlier stages sent nothing.
	unsent := toolcontract.NoEffect
	afterSend := toolcontract.EffectUnknown
	if received {
		afterSend = toolcontract.EffectCompleted
	}
	switch {
	case errors.As(cause, &dns):
		return &curlFailure{code: 6, message: "Could not resolve host: " + dns.Name, certainty: unsent, cause: err}
	case dial && timeout:
		return &curlFailure{code: 28, message: "Connection timed out", certainty: unsent, cause: err}
	case dial:
		return &curlFailure{code: 7, message: "Failed to connect", certainty: unsent, cause: err}
	case errors.As(cause, &tlsFault):
		return &curlFailure{code: 60, message: "SSL certificate problem: " + tlsFault.Reason, certainty: unsent, cause: err}
	case errors.As(cause, &redirect) && redirect.LimitExceeded:
		return &curlFailure{code: 47, message: "Maximum redirects followed", certainty: afterSend, cause: err}
	case errors.As(cause, &oversized):
		return &curlFailure{code: 63, message: "Maximum file size exceeded", certainty: toolcontract.EffectCompleted, cause: err}
	case errors.As(cause, &partial):
		return &curlFailure{code: 18, message: "Transferred a partial file", certainty: toolcontract.EffectCompleted, cause: err}
	case timeout:
		return &curlFailure{code: 28, message: "Operation timed out", certainty: afterSend, cause: err}
	case alert:
		return &curlFailure{code: 56, message: "Failure when receiving data from the peer: " + op.Error(), certainty: afterSend, cause: err}
	case started && !received:
		return &curlFailure{code: 52, message: "Empty reply from server", certainty: toolcontract.EffectUnknown, cause: err}
	case !started:
		return &curlFailure{code: 7, message: "Failed to connect", certainty: unsent, cause: err}
	}
	return &curlFailure{code: 56, message: "Failure when receiving data from the peer", certainty: afterSend, cause: err}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// settle turns the run into curl's producer outcome. A host egress denial,
// owner refusal or invalid form stops the program with the owner's code; a
// transport or --fail outcome is curl's ordinary exit status.
func (inv *invocation) settle(err error) (toolcontract.ProducerResult, error) {
	if err == nil {
		if err = inv.writeOut(0); err == nil {
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted,
				Evidence: inv.evidence()}, nil
		}
		return inv.failure(err)
	}
	result, err := inv.failure(err)
	_ = inv.writeOut(result.Status)
	return result, err
}

// failure settles an unsuccessful run as a host refusal or curl's exit.
func (inv *invocation) failure(err error) (toolcontract.ProducerResult, error) {
	if _, denied := egressgate.Denied(inv.ctx); denied || errors.Is(err, context.Canceled) && !inv.sent {
		return inv.refused(err, 7)
	}
	var transport *httpaction.TransportFailure
	var failure *curlFailure
	if !errors.As(err, &transport) && !errors.As(err, &failure) {
		var pathErr *projectpaths.PathError
		if toolrejection.AsToolReject(err) != nil || errors.As(err, &pathErr) {
			return inv.refused(err, 2)
		}
	}
	var dns *net.DNSError
	if transport != nil && transport.Reject != nil && (transport.Reject.Code == "HTTP_REQUEST_HOST_DENIED" || transport.Reject.Code == isolation.CodeTryLoopbackConnect) && !errors.As(err, &dns) {
		return inv.refused(err, 7)
	}
	code, result := TransportOutcome(err)
	if failure == nil || failure.message == "" {
		failure = classify(err)
	}
	if !inv.opts.silent || inv.opts.showError {
		_, _ = fmt.Fprintf(inv.stderr, "curl: (%d) %s\n", code, failure.message)
	}
	result.Evidence = inv.evidence()
	return result, err
}

func (inv *invocation) refused(err error, code int) (toolcontract.ProducerResult, error) {
	stop := "COMMAND_OPERAND_INVALID"
	if reject := toolrejection.AsToolReject(err); reject != nil {
		stop = reject.Code
	}
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
		stop = pathErr.Code
		if toolrejection.AsToolReject(err) == nil {
			reason := pathErr.Error()
			if pathErr.Cause != nil {
				reason = pathErr.Cause.Error()
			}
			err = errors.Join(err, &toolrejection.ToolReject{Code: pathErr.Code, Data: map[string]any{
				"command": "curl", "path": pathErr.Operand, "reason": reason,
				"limit_id": pathErr.LimitID, "maximum": pathErr.Maximum, "observed": pathErr.Observed, "resource_unit": pathErr.ResourceUnit,
			}})
		}
	}
	if errors.Is(err, context.Canceled) {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, err
	}
	_, _ = fmt.Fprintf(inv.stderr, "curl: %s [%s]\n", inv.opts.url, stop)
	return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerRefused, StopCode: stop, EffectCertainty: toolcontract.NoEffect}, err
}

// evidence names the owner and the sent, received and emitted body
// identities.
func (inv *invocation) evidence() toolcontract.OperationEvidence {
	evidence := toolcontract.OperationEvidence{Owner: "outbound_http", Kind: "http_exchange"}
	var refs []string
	if inv.transfer.SentBytes > 0 {
		refs = append(refs, "sent:sha256:"+inv.transfer.SentSHA256)
	}
	if inv.transfer.ReceivedSHA256 != "" {
		refs = append(refs, "received:sha256:"+inv.transfer.ReceivedSHA256, "emitted:sha256:"+inv.transfer.EmittedSHA256)
	}
	evidence.EvidenceRef = strings.Join(refs, " ")
	return evidence
}
