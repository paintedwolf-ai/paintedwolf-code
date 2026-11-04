package providerwire

import (
	"crypto/sha256"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/tokenest"
)

// First-difference causes, derived from structured request and host facts.
const (
	prefixFirstRequest    = "first_request"
	prefixNoChange        = "none"
	prefixAppended        = "history_appended"
	prefixToolsChanged    = "tools_changed"
	prefixStandingChanged = "standing_changed"
	prefixTailChanged     = "tail_injects_changed"
	prefixSummaryChanged  = "summary_published"
	prefixRowChanged      = "history_row_changed"
)

// Method labels. Token counts cover byte-identical serialized segments, so they
// measure reuse at segment granularity rather than a provider's rendered prompt.
const prefixProxyMethod = "serialization_proxy_bytes"

type prefixSegment struct {
	kind   modelcall.ModelInputKind
	digest [sha256.Size]byte
}

// prefixBaseline retains digests only; prompt content never outlives the request.
type prefixBaseline struct {
	segments []prefixSegment
	cut      modelcall.HistoryCutFacts
	changes  int
}

type prefixMeasurement struct {
	baseline                  prefixBaseline
	method, firstDifference   string
	inputBytes, reusableBytes int
	reusableTokens            int
	cutoffChange              string
}

func (m prefixMeasurement) apply(out *observability.PromptCacheObservation) {
	out.PrefixMethod = m.method
	out.ModelInputBytes = m.inputBytes
	out.ReusablePrefixBytes = m.reusableBytes
	out.ReusablePrefixTokens = m.reusableTokens
	out.FirstDifference = m.firstDifference
	out.CutoffChange = m.cutoffChange
	out.CutoffChanges = m.baseline.changes
}

// measurePrefixReuse compares the final serialized model input with the previous
// request's. It reports what could be reused, never what a provider did reuse.
func measurePrefixReuse(prev prefixBaseline, found bool, req modelcall.CompletionRequest, identity promptCacheIdentity, out observability.PromptCacheObservation) prefixMeasurement {
	input := req.ControlCapture.ModelInput()
	measured := prefixMeasurement{cutoffChange: req.Debug.HistoryCut.Change}
	measured.baseline.cut = req.Debug.HistoryCut
	measured.baseline.changes = prev.changes
	if found && cutChanged(prev.cut, req.Debug.HistoryCut) {
		measured.baseline.changes++
	}
	common := 0
	measured.baseline.segments = make([]prefixSegment, len(input))
	for i, segment := range input {
		measured.baseline.segments[i] = prefixSegment{kind: segment.Kind, digest: segment.Digest}
		measured.inputBytes += len(segment.Text)
		if common == i && i < len(prev.segments) && prev.segments[i].digest == segment.Digest {
			common = i + 1
			measured.reusableBytes += len(segment.Text)
		}
	}
	if len(input) > 0 {
		measured.method = prefixProxyMethod
		if counter, ok := prefixTokenCounter(req.Model); ok {
			if tokens, err := countSegments(counter, input[:common]); err == nil {
				measured.method, measured.reusableTokens = "tokens:"+counter.Method(), tokens
			}
		}
	}
	measured.firstDifference = firstDifferenceCause(prev, found, measured.baseline, common, identity, out)
	return measured
}

func prefixTokenCounter(model string) (tokenest.Counter, bool) {
	encoding := modelinfo.DefaultModelContextWindows().TextEncoding(model)
	if encoding == "" {
		return tokenest.Counter{}, false
	}
	counter, err := tokenest.NewCounter(encoding)
	return counter, err == nil
}

func countSegments(counter tokenest.Counter, segments []modelcall.ModelInputSegment) (int, error) {
	total := 0
	for _, segment := range segments {
		n, err := counter.Count(string(segment.Text))
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func cutChanged(prev, cur modelcall.HistoryCutFacts) bool {
	return prev.TailOrd != cur.TailOrd || prev.WorkingSetOrd != cur.WorkingSetOrd || prev.FitOrd != cur.FitOrd || prev.BodiesOrd != cur.BodiesOrd || prev.Generation != cur.Generation
}

func firstDifferenceCause(prev prefixBaseline, found bool, cur prefixBaseline, common int, identity promptCacheIdentity, out observability.PromptCacheObservation) string {
	if !found {
		return prefixFirstRequest
	}
	whole := common == len(prev.segments) && len(prev.segments) > 0
	if whole && common == len(cur.segments) {
		return prefixNoChange
	}
	if whole {
		return prefixAppended
	}
	switch {
	case common < len(prev.segments) && prev.segments[common].kind == modelcall.ModelInputTools,
		common < len(cur.segments) && cur.segments[common].kind == modelcall.ModelInputTools,
		out.ToolsChanged:
		return prefixToolsChanged
	case common < len(prev.segments) && prev.segments[common].kind == modelcall.ModelInputSystem:
		return prefixStandingChanged
	case prev.cut.Generation != cur.cut.Generation:
		return prefixSummaryChanged
	case out.StandingChanged:
		return prefixStandingChanged
	case cutChanged(prev.cut, cur.cut):
		if cur.cut.Change != "" {
			return cur.cut.Change
		}
		return prefixRowChanged
	case !out.HistoryChanged:
		if len(identity.history) > 0 && len(cur.segments) > len(prev.segments) {
			return prefixAppended
		}
		return prefixTailChanged
	default:
		return prefixRowChanged
	}
}
