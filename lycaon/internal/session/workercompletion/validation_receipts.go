package workercompletion

import (
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/verification"
)

// currentValidationReceipts selects the last settled result of each current check.
func currentValidationReceipts(proof WorkerCompletionProof) []WorkerInvocationReceipt {
	var out []WorkerInvocationReceipt
	indices := make(map[string]int)
	for _, receipt := range proof.InvocationReceipts {
		if receipt.SourceRevision != proof.SourceRevision || receipt.SourceRootDigest != proof.SourceRootDigest {
			continue
		}
		if proof.Verification.Valid() && proof.Verification.Method == verification.Inspection {
			if receipt.Tool == "complete_leg" {
				out = append(out, receipt)
			}
			continue
		}
		if !sourceRunReceipt(receipt) || receipt.Verdict == "" {
			continue
		}
		key := commandsurface.CheckKey(receipt.Command, receipt.Cwd)
		if i, ok := indices[key]; ok {
			out[i] = receipt
		} else {
			indices[key] = len(out)
			out = append(out, receipt)
		}
	}
	return out
}
