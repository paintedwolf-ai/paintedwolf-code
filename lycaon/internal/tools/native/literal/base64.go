package literal

import (
	"encoding/base64"
	"strconv"
)

// base64Width is the GNU -w or BSD -b wrap column; zero disables wrapping.
func (inv *invocation) base64Width() (int64, error) {
	value, which := inv.lastOf("-w", "-b")
	if which == "" {
		return 76, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, inv.usage(value, "nonnegative wrap width for "+which)
	}
	return n, nil
}

func runBase64(inv *invocation) error {
	width, err := inv.base64Width()
	if err != nil {
		return err
	}
	in, err := inv.singleInput()
	if err != nil || in == nil {
		return err
	}
	if inv.flag("-d") {
		return decodeBase64(inv, in.data)
	}
	encoded := base64.StdEncoding.EncodeToString(in.data)
	if width == 0 || len(encoded) == 0 {
		return inv.out.text(encoded)
	}
	out := make([]byte, 0, len(encoded)+len(encoded)/int(width)+1)
	for len(encoded) > 0 {
		line := encoded[:min(int64(len(encoded)), width)]
		out = append(append(out, line...), '\n')
		encoded = encoded[len(line):]
	}
	_, err = inv.out.Write(out)
	return err
}

// decodeBase64 ignores ASCII whitespace and refuses a malformed alphabet or
// padding without emitting a partial decode.
func decodeBase64(inv *invocation, data []byte) error {
	compact := make([]byte, 0, len(data))
	for _, c := range data {
		switch c {
		case ' ', '\t', '\n', '\r', '\v', '\f':
		default:
			compact = append(compact, c)
		}
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(string(compact))
	if err != nil {
		inv.fail(1, "", "invalid input")
		return nil //nolint:nilerr // Invalid base64 settles the ordinary status-1 result without emitting a partial decode.
	}
	_, err = inv.out.Write(decoded)
	return err
}
