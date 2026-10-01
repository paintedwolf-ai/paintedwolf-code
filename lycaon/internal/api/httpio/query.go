package httpio

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

type QueryParameterError struct {
	Parameter string
	Reason    string
}

func (e *QueryParameterError) Error() string {
	return fmt.Sprintf("%s %s", e.Parameter, e.Reason)
}

func SingleQueryValue(r *http.Request, parameter string) (string, bool, error) {
	values, present := r.URL.Query()[parameter]
	if !present {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", true, &QueryParameterError{Parameter: parameter, Reason: "must be supplied at most once"}
	}
	if values[0] == "" {
		return "", true, &QueryParameterError{Parameter: parameter, Reason: "must not be empty"}
	}
	return values[0], true, nil
}

func OptionalBoolQuery(r *http.Request, parameter string) (bool, bool, error) {
	raw, present, err := SingleQueryValue(r, parameter)
	if err != nil || !present {
		return false, present, err
	}
	switch raw {
	case "true":
		return true, true, nil
	case "false":
		return false, true, nil
	default:
		return false, true, &QueryParameterError{Parameter: parameter, Reason: "must be true or false"}
	}
}

func OptionalIntQuery(r *http.Request, parameter string, minValue, maxValue int) (int, bool, error) {
	raw, present, err := SingleQueryValue(r, parameter)
	if err != nil || !present {
		return 0, present, err
	}
	value, parseErr := strconv.Atoi(raw)
	if parseErr != nil || value < minValue || (maxValue > 0 && value > maxValue) {
		reason := fmt.Sprintf("must be an integer greater than or equal to %d", minValue)
		if maxValue > 0 {
			reason = fmt.Sprintf("must be an integer between %d and %d", minValue, maxValue)
		}
		return 0, true, &QueryParameterError{Parameter: parameter, Reason: reason}
	}
	return value, true, nil
}

func OptionalInt64Query(r *http.Request, parameter string, minValue int64) (int64, bool, error) {
	raw, present, err := SingleQueryValue(r, parameter)
	if err != nil || !present {
		return 0, present, err
	}
	value, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil || value < minValue {
		return 0, true, &QueryParameterError{
			Parameter: parameter,
			Reason:    fmt.Sprintf("must be an integer greater than or equal to %d", minValue),
		}
	}
	return value, true, nil
}

func OptionalUint64Query(r *http.Request, parameter string, minValue uint64) (uint64, bool, error) {
	raw, present, err := SingleQueryValue(r, parameter)
	if err != nil || !present {
		return 0, present, err
	}
	value, parseErr := strconv.ParseUint(raw, 10, 64)
	if parseErr != nil || value < minValue {
		return 0, true, &QueryParameterError{
			Parameter: parameter,
			Reason:    fmt.Sprintf("must be an integer greater than or equal to %d", minValue),
		}
	}
	return value, true, nil
}

// EncodedPathID decodes a single route segment so composite IDs retain embedded slashes.
func EncodedPathID(r *http.Request, name string) string {
	raw := chi.URLParam(r, name)
	if raw == "" {
		return ""
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(decoded)
}
