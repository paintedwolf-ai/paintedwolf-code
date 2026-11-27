// Package capabilitydrift checks delivered guidance against the decisions the
// owners of the advertised actions make for the same request.
package capabilitydrift

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/rejectinventory"
)

// Instruction is one exact action a delivered request recommends.
type Instruction struct {
	Recipient string
	State     string
	Block     string
	Call      string
	Tool      string
	Offered   bool
}

// DeliveredInstructions reads the host-authored blocks of one captured
// request and returns each call expression that names a tool in known.
func DeliveredInstructions(recipient, state string, req modelcall.CompletionRequest, known func(string) bool) []Instruction {
	offered := map[string]bool{}
	for _, meta := range req.Tools {
		offered[meta.Name] = true
	}
	var out []Instruction
	for _, msg := range req.Messages {
		if msg.Origin != api.MessageOriginHost {
			continue
		}
		for _, block := range toolvocab.Blocks(msg.Content) {
			for _, call := range toolvocab.CallsOf(known, block.Text) {
				out = append(out, Instruction{
					Recipient: recipient, State: state, Block: block.Text,
					Call: block.Text[call.Start:call.End], Tool: call.Tool, Offered: offered[call.Tool],
				})
			}
		}
	}
	return out
}

// Contradiction is a delivered instruction whose exact action the owner
// denied on the same request.
type Contradiction struct {
	Instruction Instruction
	Witness     rejectinventory.Witness
}

func (c Contradiction) String() string {
	return fmt.Sprintf("recipient %s state %s: instruction %q recommends %s; decision %s denied it with %s (receipt %s, invoked=%t)",
		c.Instruction.Recipient, c.Instruction.State, c.Instruction.Block, c.Instruction.Call,
		c.Witness.Decision, c.Witness.Code, c.Witness.Status, c.Witness.Invoked)
}

// Contradictions joins delivered instructions to witnessed decisions for the
// same recipient, request state and exact call. Only a request-time admission
// denial contradicts an instruction.
func Contradictions(instructions []Instruction, witnesses []rejectinventory.Witness) []Contradiction {
	var out []Contradiction
	for _, in := range instructions {
		for _, w := range witnesses {
			if w.Recipient != in.Recipient || w.State != in.State || w.Tool != in.Tool {
				continue
			}
			if strings.Trim((toolreach.Call{Tool: w.Tool, Args: w.Inputs}).Text(), "`") != in.Call {
				continue
			}
			if AdmissionDenial(w) {
				out = append(out, Contradiction{Instruction: in, Witness: w})
			}
		}
	}
	return out
}

// AdmissionDenial reports a refusal that withdraws availability on the request
// it was decided for: a policy or isolation refusal settled before the owner
// took effect. A person's decision, a retryable isolation outcome such as a
// changed authorization, and an owner's revalidation after it started do not.
func AdmissionDenial(w rejectinventory.Witness) bool {
	if w.Invoked || w.Status != string(api.InvocationStatusRejected) {
		return false
	}
	if w.FailureClass != api.FailureClassPolicyRejection && w.FailureClass != api.FailureClassIsolationRejection {
		return false
	}
	if slices.Contains([]string{approvaloutcome.CodeApprovalDenied, approvaloutcome.CodeApprovalExpired, approvaloutcome.CodeApprovalCanceled}, w.Code) {
		return false
	}
	switch isolation.Disposition(w.Disposition) {
	case isolation.DispositionHumanDecision, isolation.DispositionRetry:
		return false
	default:
	}
	return true
}
