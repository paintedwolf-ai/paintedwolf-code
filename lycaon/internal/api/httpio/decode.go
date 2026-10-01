package httpio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/textfile"
)

const MaxJSONBody = 1 << 20 // 1 MiB

const (
	MediaTypeJSON = "application/json"
	MediaTypeYAML = "application/yaml"
)

func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return DecodeJSONLimit(w, r, dst, MaxJSONBody)
}

// DecodeJSONWithRaw validates one JSON value and retains its raw field presence.
func DecodeJSONWithRaw(w http.ResponseWriter, r *http.Request, dst, raw any) error {
	if err := RequireRequestMediaType(r, MediaTypeJSON); err != nil {
		return err
	}
	body, err := ReadAllBody(w, r)
	if err != nil {
		return err
	}
	if err := DecodeStrictJSON(bytes.NewReader(body), dst); err != nil {
		return err
	}
	return json.Unmarshal(body, raw)
}

// DecodeStrictJSON decodes exactly one JSON value into dst, rejecting fields
// outside the wire contract.
func DecodeStrictJSON(body io.Reader, dst any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return ErrMultipleJSONValues
		}
		return err
	}
	return nil
}

func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	if err := RequireRequestMediaType(r, MediaTypeJSON); err != nil {
		return err
	}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := DecodeStrictJSON(r.Body, dst); err != nil {
		if IsBodyTooLarge(err) {
			return ErrBodyTooLarge
		}
		return err
	}
	return nil
}

// DecodeOptionalJSON decodes a body the operation declares optional. An absent
// or empty body leaves dst unchanged and reports false; a present one is
// bounded, typed, and strictly decoded like DecodeJSON.
func DecodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) (bool, error) {
	body, err := ReadAllBody(w, r)
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return false, nil
	}
	if err := RequireRequestMediaType(r, MediaTypeJSON); err != nil {
		return false, err
	}
	if err := DecodeStrictJSON(bytes.NewReader(body), dst); err != nil {
		return false, err
	}
	return true, nil
}

// ReadAllBody retains bytes for typed and raw decoding.
func ReadAllBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		if IsBodyTooLarge(err) {
			return nil, ErrBodyTooLarge
		}
		return nil, err
	}
	return data, nil
}

var (
	ErrBodyTooLarge         = errors.New("request body too large")
	ErrMultipleJSONValues   = errors.New("request body must contain exactly one JSON value")
	ErrUnsupportedMediaType = errors.New("unsupported request media type")
)

// RequireRequestMediaType validates the representation of requests that
// actually carry bytes. Bodyless commands remain valid without Content-Type.
func RequireRequestMediaType(r *http.Request, allowed ...string) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	raw := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return ErrUnsupportedMediaType
	}
	for _, candidate := range allowed {
		if mediaType == candidate {
			return nil
		}
	}
	return ErrUnsupportedMediaType
}

const StatusClientClosedRequest = 499

func IsBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// Source text can expand during UTF-16 decoding and JSON escaping. The
// source service separately enforces decoded and encoded content limits.
func DecodeSourceJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	limit := 6*textfile.LimitsForRaw(project.SourceWriteMaxBytes).MaxTextBytes + MaxJSONBody
	return DecodeJSONLimit(w, r, dst, limit)
}
