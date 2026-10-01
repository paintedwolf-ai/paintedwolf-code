package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxOpengrepCallDepth = 64

type opengrepTraceLocation struct {
	Path  string           `json:"path"`
	Start opengrepPosition `json:"start"`
	End   opengrepPosition `json:"end"`
}

func (l opengrepTraceLocation) location() (api.SecurityFindingLocation, error) {
	if l.Path == "" || l.Start.Line < 1 || l.Start.Col < 1 || l.End.Line < l.Start.Line || l.End.Col < 1 || (l.End.Line == l.Start.Line && l.End.Col < l.Start.Col) {
		return api.SecurityFindingLocation{}, fmt.Errorf("invalid Opengrep trace location")
	}
	return api.SecurityFindingLocation{URI: l.Path, StartLine: l.Start.Line, StartColumn: l.Start.Col, EndLine: l.End.Line, EndColumn: l.End.Col}, nil
}

type opengrepTraceVariable struct {
	Location opengrepTraceLocation `json:"location"`
}

type opengrepDataflow struct {
	Source        json.RawMessage         `json:"taint_source"`
	Intermediates []opengrepTraceVariable `json:"intermediate_vars"`
	Sink          json.RawMessage         `json:"taint_sink"`
}

func (t *opengrepDataflow) evidence() (*api.SecurityFindingDataflow, error) {
	if t == nil {
		return nil, nil
	}
	source, err := decodeOpengrepCallTrace(t.Source, 0)
	if err != nil {
		return nil, fmt.Errorf("taint source: %w", err)
	}
	sink, err := decodeOpengrepCallTrace(t.Sink, 0)
	if err != nil {
		return nil, fmt.Errorf("taint sink: %w", err)
	}
	steps, err := opengrepTraceVariables(t.Intermediates)
	if err != nil {
		return nil, err
	}
	return &api.SecurityFindingDataflow{Source: source, Sink: sink, Intermediates: steps}, nil
}

func decodeOpengrepCallTrace(raw json.RawMessage, depth int) (*api.SecurityFindingCallTrace, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	if depth >= maxOpengrepCallDepth {
		return nil, fmt.Errorf("opengrep call trace exceeds %d nested calls", maxOpengrepCallDepth)
	}
	var variant []json.RawMessage
	if err := json.Unmarshal(raw, &variant); err != nil || len(variant) != 2 {
		return nil, fmt.Errorf("invalid Opengrep call trace envelope")
	}
	var tag string
	if err := json.Unmarshal(variant[0], &tag); err != nil {
		return nil, fmt.Errorf("invalid Opengrep call trace tag")
	}
	if tag == "CliLoc" {
		location, err := decodeOpengrepLocationContent(variant[1])
		return &api.SecurityFindingCallTrace{Location: location}, err
	}
	if tag != "CliCall" {
		return nil, fmt.Errorf("unknown Opengrep call trace tag %q", tag)
	}
	return decodeOpengrepCall(variant[1], depth)
}

func decodeOpengrepCall(raw json.RawMessage, depth int) (*api.SecurityFindingCallTrace, error) {
	var call []json.RawMessage
	if err := json.Unmarshal(raw, &call); err != nil || len(call) != 3 {
		return nil, fmt.Errorf("invalid Opengrep call tuple")
	}
	location, err := decodeOpengrepLocationContent(call[0])
	if err != nil {
		return nil, err
	}
	var variables []opengrepTraceVariable
	if err := json.Unmarshal(call[1], &variables); err != nil {
		return nil, fmt.Errorf("call variables: %w", err)
	}
	steps, err := opengrepTraceVariables(variables)
	if err != nil {
		return nil, err
	}
	callee, err := decodeOpengrepCallTrace(call[2], depth+1)
	if err != nil {
		return nil, err
	}
	if callee == nil {
		return nil, fmt.Errorf("opengrep call trace has no callee")
	}
	return &api.SecurityFindingCallTrace{Location: location, Intermediates: steps, Callee: callee}, nil
}

func decodeOpengrepLocationContent(raw json.RawMessage) (api.SecurityFindingLocation, error) {
	var pair []json.RawMessage
	if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
		return api.SecurityFindingLocation{}, fmt.Errorf("invalid Opengrep location/content tuple")
	}
	var location opengrepTraceLocation
	if err := json.Unmarshal(pair[0], &location); err != nil {
		return api.SecurityFindingLocation{}, err
	}
	return location.location()
}

func opengrepTraceVariables(vars []opengrepTraceVariable) ([]api.SecurityFindingLocation, error) {
	var locations []api.SecurityFindingLocation
	for _, v := range vars {
		location, err := v.Location.location()
		if err != nil {
			return nil, err
		}
		if len(locations) == 0 || locations[len(locations)-1] != location {
			locations = append(locations, location)
		}
	}
	return locations, nil
}
