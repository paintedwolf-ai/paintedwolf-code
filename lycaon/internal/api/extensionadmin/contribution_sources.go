package extensionadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/commandinvoke"
	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const contributionProviderEnvelopeBytes = 256 << 10
const contributionProviderTimeout = 20 * time.Second

func (s *Contributions) HandleContributionChoices(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	var req wire.ContributionChoiceRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	frame, ok := s.contributionSourceFrame(w, r, p, req.FrameRevision)
	if !ok {
		return
	}
	commandID, err := contribution.ParseID(httpio.EncodedPathID(r, "command_id"))
	if err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "command_id", "reason": "is not a contribution id"}, "command_id is not a contribution id")
		return
	}
	command, found := frame.Command(commandID)
	if !found {
		s.responses.Fail(w, wire.ApiErrorCodeCommandNotFound, "command "+commandID.String()+" is not in the current contribution frame")
		return
	}
	stepIndex := -1
	if command.Interaction != nil {
		stepID := httpio.EncodedPathID(r, "step_id")
		for index := range command.Interaction.Steps {
			if command.Interaction.Steps[index].ID == stepID {
				stepIndex = index
				break
			}
		}
	}
	if stepIndex < 0 || command.Interaction.Steps[stepIndex].Source == nil {
		s.responses.Fail(w, wire.ApiErrorCodeChoiceSourceNotFound, "interaction step has no dynamic source")
		return
	}
	if !s.requireFrameRevision(w, frame, req.FrameRevision) {
		return
	}
	step := command.Interaction.Steps[stepIndex]
	fields := contribution.InteractionFieldsForAnswers(&contribution.Interaction{Steps: command.Interaction.Steps[:stepIndex]}, req.Answers)
	resolvePath := projectPathResolver(p)
	answers, err := commandinvoke.ValidateArguments(fields, req.Answers, resolvePath)
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidArguments, "the command arguments are invalid")
		return
	}
	args := map[string]any{}
	for argument, answerID := range step.Source.Inputs {
		args[argument] = answers[answerID]
	}
	requirement, providerID, ok := s.runnableContributionTool(w, frame, step.Source.Requirement, step.Source.Tool)
	if !ok {
		return
	}
	callContext, cancel := context.WithTimeout(r.Context(), contributionProviderTimeout)
	defer cancel()
	output, err := s.MCPCalls.CallTool(callContext, mcp.ProjectScope(p.ID, project.PrimaryRootPath(p), project.RootPaths(p)), requirement.ProviderID, step.Source.Tool, args)
	if err != nil {
		s.responses.Logger.WarnContext(r.Context(), "contribution choice source failed", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeChoiceSourceFailed, "the choice provider failed")
		return
	}
	choices, err := decodeProviderChoices(output)
	if err != nil {
		s.responses.Logger.WarnContext(r.Context(), "contribution choice source answered an invalid envelope", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeChoiceSourceInvalid, "the choice provider answered an invalid envelope")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ContributionChoiceResponse{FrameRevision: frame.Revision, Provider: providerID, Choices: choices})
}

func (s *Contributions) HandleContributionSearch(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	var req wire.ContributionSearchRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	frame, ok := s.contributionSourceFrame(w, r, p, req.FrameRevision)
	if !ok {
		return
	}
	sourceID, err := contribution.ParseID(httpio.EncodedPathID(r, "source_id"))
	if err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "source_id", "reason": "is not a contribution id"}, "source_id is not a contribution id")
		return
	}
	source, found := frame.View.Contributions.SearchSource(sourceID)
	if !found {
		s.responses.Fail(w, wire.ApiErrorCodeSearchSourceNotFound, "search source is not in the captured frame")
		return
	}
	if !s.requireFrameRevision(w, frame, req.FrameRevision) {
		return
	}
	query := strings.TrimSpace(req.Query)
	if utf8.RuneCountInString(query) < source.Query.MinLength || utf8.RuneCountInString(query) > 4096 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidQuery, "query length is outside the source bounds")
		return
	}
	requirement, providerID, ok := s.runnableContributionTool(w, frame, source.Requirement, source.Tool)
	if !ok {
		return
	}
	callContext, cancel := context.WithTimeout(r.Context(), contributionProviderTimeout)
	defer cancel()
	output, err := s.MCPCalls.CallTool(callContext, mcp.ProjectScope(p.ID, project.PrimaryRootPath(p), project.RootPaths(p)), requirement.ProviderID, source.Tool, map[string]any{"query": query, "limit": source.Query.MaxResults})
	if err != nil {
		s.responses.Logger.WarnContext(r.Context(), "contribution search source failed", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeContributionSearchFailed, "the search provider failed")
		return
	}
	results, err := decodeProviderSearchResults(output, source)
	if err != nil {
		s.responses.Logger.WarnContext(r.Context(), "contribution search source answered an invalid envelope", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeContributionSearchInvalid, "the search provider answered an invalid envelope")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ContributionSearchResponse{FrameRevision: frame.Revision, SourceID: source.ID, Provider: providerID, Results: results})
}

// contributionSourceFrame captures the current frame. Callers resolve the
// addressed contribution in it before comparing the caller's revision, so an
// unknown address answers not found whatever revision the request carries.
func (s *Contributions) contributionSourceFrame(w http.ResponseWriter, r *http.Request, p *project.Project, revision string) (*contribframe.Frame, bool) {
	if strings.TrimSpace(revision) == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "frame_revision is required")
		return nil, false
	}
	frame, err := s.CaptureContributionFrame(r.Context(), p.ID, project.PrimaryRootPath(p))
	if err != nil {
		s.writeContributionFrameError(w, r, err)
		return nil, false
	}
	return frame, true
}

func (s *Contributions) requireFrameRevision(w http.ResponseWriter, frame *contribframe.Frame, revision string) bool {
	if frame.Revision != revision {
		s.responses.Fail(w, wire.ApiErrorCodeContributionFrameChanged, "the contribution frame changed; refresh before requesting provider data")
		return false
	}
	return true
}

func (s *Contributions) runnableContributionTool(w http.ResponseWriter, frame *contribframe.Frame, requirementRaw, toolName string) (*contribution.MCPRequirement, string, bool) {
	requirementID, err := contribution.ParseID(requirementRaw)
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeContributionSourceUnavailable, "the source requirement id is invalid")
		return nil, "", false
	}
	status, found := frame.Requirement(requirementID)
	requirement, declared := frame.View.Contributions.MCPRequirement(requirementID)
	if !found || !declared || !status.Ready {
		s.responses.Fail(w, wire.ApiErrorCodeContributionSourceUnavailable, "the source requirement is not ready")
		return nil, "", false
	}
	tool, found := frame.MCP.Tool(requirement.ProviderID, toolName)
	if !found || !tool.ReadOnly {
		s.responses.Fail(w, wire.ApiErrorCodeContributionSourceUnavailable, "provider-backed choices and search require a read-only tool")
		return nil, "", false
	}
	return requirement, requirement.ProviderID, true
}

func projectPathResolver(p *project.Project) commandinvoke.ProjectPathResolver {
	if p == nil {
		return nil
	}
	return func(rootID, path string) (string, string, error) {
		resolved, err := project.ResolveProjectPath(p, rootID, path)
		return resolved.RootID, resolved.Path, err
	}
}

func decodeProviderChoices(raw string) ([]wire.ContributionChoice, error) {
	var envelope struct {
		Choices []wire.ContributionChoice `json:"choices"`
	}
	if err := decodeBoundedProviderJSON(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Choices) > contribution.MaxChoicesPerStep {
		return nil, fmt.Errorf("provider returned too many choices")
	}
	seen := map[string]bool{}
	for _, choice := range envelope.Choices {
		if strings.TrimSpace(choice.ID) == "" || strings.TrimSpace(choice.Label) == "" || seen[choice.ID] {
			return nil, fmt.Errorf("choices require unique non-empty id and label")
		}
		seen[choice.ID] = true
		if !boundedProviderText(choice.Label, 256) || !boundedProviderText(choice.Description, 1024) || !boundedProviderText(choice.Detail, 2048) {
			return nil, fmt.Errorf("choice text exceeds its host bound")
		}
	}
	return envelope.Choices, nil
}

func decodeProviderSearchResults(raw string, source *contribution.SearchSource) ([]wire.ContributionSearchResult, error) {
	var envelope struct {
		Results []map[string]any `json:"results"`
	}
	if err := decodeBoundedProviderJSON(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Results) > source.Query.MaxResults {
		return nil, fmt.Errorf("provider returned too many results")
	}
	out := make([]wire.ContributionSearchResult, 0, len(envelope.Results))
	seen := map[string]bool{}
	allowedFields := map[string]bool{}
	for _, field := range []string{source.Result.ID, source.Result.Title, source.Result.Description, source.Result.Detail, source.Result.Arguments} {
		if field != "" {
			allowedFields[field] = true
		}
	}
	for _, rawResult := range envelope.Results {
		for field := range rawResult {
			if !allowedFields[field] {
				return nil, fmt.Errorf("result contains undeclared field %q", field)
			}
		}
		id, _ := rawResult[source.Result.ID].(string)
		title, _ := rawResult[source.Result.Title].(string)
		if strings.TrimSpace(id) == "" || strings.TrimSpace(title) == "" || seen[id] {
			return nil, fmt.Errorf("results require unique non-empty id and title")
		}
		seen[id] = true
		row := wire.ContributionSearchResult{ID: id, Title: title, Kind: source.Result.Kind}
		row.Description, _ = rawResult[source.Result.Description].(string)
		row.Detail, _ = rawResult[source.Result.Detail].(string)
		if source.Result.Arguments != "" {
			var argumentsOK bool
			row.Arguments, argumentsOK = rawResult[source.Result.Arguments].(map[string]any)
			if !argumentsOK {
				return nil, fmt.Errorf("result %q activation arguments must be an object", id)
			}
			validated, err := commandinvoke.ValidateArguments(contribution.InputFieldsForOutput(source.Activation.Input), row.Arguments, nil)
			if err != nil {
				return nil, fmt.Errorf("result %q activation arguments: %w", id, err)
			}
			row.Arguments = validated
		} else if len(source.Activation.Input) > 0 {
			return nil, fmt.Errorf("result %q has no activation arguments", id)
		}
		if !boundedProviderText(row.Title, 256) || !boundedProviderText(row.Description, 1024) || !boundedProviderText(row.Detail, 2048) {
			return nil, fmt.Errorf("result text exceeds its host bound")
		}
		out = append(out, row)
	}
	return out, nil
}

func decodeBoundedProviderJSON(raw string, out any) error {
	if len(raw) > contributionProviderEnvelopeBytes {
		return fmt.Errorf("provider response exceeds the host byte bound")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("provider response is not the declared JSON envelope: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("provider response contains trailing data")
	}
	return nil
}

func boundedProviderText(value string, max int) bool { return utf8.RuneCountInString(value) <= max }
