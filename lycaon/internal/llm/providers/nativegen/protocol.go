package nativegen

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxFrameBytes = 8 << 20

type command struct {
	Method            string `json:"method"`
	Request           any    `json:"request,omitempty"`
	RequestID         uint64 `json:"request_id,omitempty"`
	ContextTokens     int    `json:"context_tokens,omitempty"`
	PrefillChunk      int    `json:"prefill_chunk,omitempty"`
	MaxPromptTokens   int    `json:"max_prompt_tokens,omitempty"`
	DeviceBudgetBytes uint64 `json:"device_budget_bytes,omitempty"`
	HostBudgetBytes   uint64 `json:"host_budget_bytes,omitempty"`
}

type response struct {
	Status  string          `json:"status"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
}

// ServiceError preserves the runtime's structured failure category.
type ServiceError struct{ Code, Message string }

func (e *ServiceError) Error() string {
	return fmt.Sprintf("native generation %s: %s", e.Code, e.Message)
}

func decodeResponse(payload []byte, id uint64, kind string) (json.RawMessage, error) {
	var reply response
	if err := decodeStrict(payload, &reply); err != nil {
		return nil, err
	}
	if reply.ID != id {
		return nil, errors.New("native generation: response sequence mismatch")
	}
	switch reply.Status {
	case "error":
		if len(reply.Result) != 0 {
			return nil, errors.New("native generation: mixed response states")
		}
		switch reply.Code {
		case "capacity", "device", "artifact", "invalid_request":
			return nil, &ServiceError{Code: reply.Code, Message: reply.Message}
		}
	case "ok":
		if reply.Code != "" || reply.Message != "" {
			return nil, errors.New("native generation: mixed response states")
		}
		var tag struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(reply.Result, &tag); err != nil {
			return nil, err
		}
		if tag.Kind != kind {
			return nil, errors.New("native generation: unexpected response kind")
		}
		return reply.Result, nil
	}
	return nil, errors.New("native generation: invalid response status")
}

func decodeStrict(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("native generation: trailing response data")
	}
	return nil
}

func readProtocolFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > maxFrameBytes {
		return nil, errors.New("native generation: frame exceeds bounds")
	}
	payload := make([]byte, size)
	_, err := io.ReadFull(reader, payload)
	return payload, err
}

func writeProtocolFrame(writer io.Writer, payload []byte) error {
	size := len(payload)
	if size <= 0 || size > maxFrameBytes {
		return errors.New("native generation: frame exceeds bounds")
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(size))
	for _, part := range [][]byte{header[:], payload} {
		for len(part) > 0 {
			n, err := writer.Write(part)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}
