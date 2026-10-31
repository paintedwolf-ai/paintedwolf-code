package curl

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// dataPart is one -d, --data-raw, --data-binary or --data-urlencode value in
// argv order; parts join with '&' as curl does.
type dataPart struct {
	kind  string
	value string
}

// writeToken is one literal run or variable of a -w format.
type writeToken struct {
	literal  string
	variable string
}

// failMode is -f/--fail or --fail-with-body: an HTTP status of 400 or more
// exits 22, without or with the body.
type failMode uint8

const (
	failNever failMode = iota
	failQuiet
	failWithBody
)

// tlsOptions name the request's added trust and client identity files.
type tlsOptions struct {
	cacert, capath string
	cert, certPass string
	key, pass      string
}

type options struct {
	url            string
	method         string
	headers        [][2]string
	data           []dataPart
	forms          []string
	user           string
	output         string
	dumpHeader     string
	cookieJar      string
	cookies        []string
	writeOut       []writeToken
	hasWriteOut    bool
	silent         bool
	showError      bool
	include        bool
	head           bool
	location       bool
	fail           failMode
	verbose        bool
	maxTime        time.Duration
	connectTimeout time.Duration
	unixSocket     string
	compressed     bool
	userAgent      string
	get            bool
	json           []string
	upload         string
	byteRange      string
	tls            tlsOptions
	dial           []outboundhttp.ResolveMapping
}

func usage(word, expected string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{"command": "curl", "word": word, "reason": expected, "expected": expected, "expected_form": "curl [OPTIONS] URL"}}
}

// parseOptions reads the bound forms in argv order. Values are data: header
// text, bodies and secret references are carried, never interpreted here.
//
//nolint:funlen // Flat switch over supported curl option flags in argv order.
func parseOptions(bound commandprogram.BoundArgv) (options, error) {
	var o options
	var connectTo []outboundhttp.ResolveMapping
	for _, item := range bound.Items {
		option := item.Option
		if option == nil {
			continue
		}
		value := ""
		if len(option.Values) > 0 {
			value = option.Values[0].Value
		}
		switch option.Key {
		case "-s":
			o.silent = true
		case "-S":
			o.showError = true
		case "-i":
			o.include = true
		case "-I":
			o.head = true
		case "-L":
			o.location = true
		case "-f", "--fail-with-body":
			mode := failQuiet
			if option.Key == "--fail-with-body" {
				mode = failWithBody
			}
			if o.fail != failNever && o.fail != mode {
				return options{}, usage(option.Spelling, "either --fail or --fail-with-body, not both")
			}
			o.fail = mode
		case "-v":
			o.verbose = true
		case "--cacert":
			o.tls.cacert = value
		case "--capath":
			o.tls.capath = value
		case "-E":
			o.tls.cert, o.tls.certPass = splitCertPassword(value)
		case "--key":
			o.tls.key = value
		case "--pass":
			o.tls.pass = value
		case "--cert-type":
			if !strings.EqualFold(value, "PEM") {
				return options{}, &toolrejection.ToolReject{Code: "COMMAND_OPTION_UNSUPPORTED", Data: map[string]any{
					"command": "curl", "word": value, "expected": "PEM; convert DER or PKCS#12 material to PEM first", "expected_form": "curl --cert-type PEM"}}
			}
		case "-T":
			o.upload = value
		case "-r":
			if !validRange(value) {
				return options{}, usage(value, "a byte range such as 0-99, 100- or -500, comma-separated")
			}
			o.byteRange = value
		case "--resolve":
			mapping, err := parseResolve(value)
			if err != nil {
				return options{}, err
			}
			o.dial = append(o.dial, mapping)
		case "--connect-to":
			mapping, err := parseConnectTo(value)
			if err != nil {
				return options{}, err
			}
			connectTo = append(connectTo, mapping)
		case "-k":
			return options{}, &toolrejection.ToolReject{Code: "COMMAND_TLS_VERIFICATION_REQUIRED", Data: map[string]any{"command": "curl", "option": option.Spelling}}
		case "--compressed":
			o.compressed = true
		case "-A":
			o.userAgent = value
		case "-G":
			o.get = true
		case "--json":
			o.json = append(o.json, value)
		case "-X":
			o.method = value
		case "-H":
			name, rest, ok := strings.Cut(value, ":")
			if !ok {
				name, ok = strings.CutSuffix(value, ";")
				if !ok {
					return options{}, usage(value, "a header as Name: value, or Name; for an empty value")
				}
				rest = ""
			}
			o.headers = append(o.headers, [2]string{strings.TrimSpace(name), strings.TrimSpace(rest)})
		case "-d":
			o.data = append(o.data, dataPart{kind: "data", value: value})
		case "--data-raw":
			o.data = append(o.data, dataPart{kind: "raw", value: value})
		case "--data-binary":
			o.data = append(o.data, dataPart{kind: "binary", value: value})
		case "--data-urlencode":
			o.data = append(o.data, dataPart{kind: "urlencode", value: value})
		case "-F":
			o.forms = append(o.forms, value)
		case "-u":
			o.user = value
		case "-o":
			o.output = value
		case "-D":
			o.dumpHeader = value
		case "-c":
			o.cookieJar = value
		case "-b":
			o.cookies = append(o.cookies, value)
		case "-w":
			tokens, err := parseWriteOut(value)
			if err != nil {
				return options{}, err
			}
			o.writeOut, o.hasWriteOut = tokens, true
		case "--max-time":
			d, err := seconds(value)
			if err != nil {
				return options{}, err
			}
			o.maxTime = d
		case "--connect-timeout":
			d, err := seconds(value)
			if err != nil {
				return options{}, err
			}
			o.connectTimeout = d
		case "--unix-socket":
			o.unixSocket = value
		}
	}
	operands := bound.Operands().Values()
	if len(operands) != 1 {
		return options{}, usage("curl", "exactly one URL")
	}
	o.url = operands[0]
	if !strings.Contains(o.url, "://") {
		o.url = "http://" + o.url
	}
	if len(o.forms) > 0 && (len(o.data) > 0 || len(o.json) > 0) {
		return options{}, usage("-F", "either -F or -d style data, not both")
	}
	if len(o.json) > 0 && len(o.data) > 0 {
		return options{}, usage("--json", "either --json or -d style data, not both")
	}
	if o.get && len(o.forms) > 0 {
		return options{}, usage("-G", "query data from -d forms, not -F parts")
	}
	if o.head && (len(o.forms) > 0 || len(o.data) > 0 || o.upload != "") {
		return options{}, usage("-I", "a HEAD request without a body")
	}
	if o.upload != "" && (len(o.forms) > 0 || len(o.data) > 0 || len(o.json) > 0) {
		return options{}, usage("-T", "either -T or -d, -F and --json bodies, not both")
	}
	if o.upload != "" && o.get {
		return options{}, usage("-G", "query data from -d forms, not an -T upload")
	}
	o.dial = composeDial(connectTo, o.dial)
	return o, nil
}

func seconds(value string) (time.Duration, error) {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || f > (time.Duration(math.MaxInt64)/time.Second).Seconds() {
		return 0, usage(value, "a nonnegative number of seconds")
	}
	return time.Duration(f * float64(time.Second)), nil
}

// writeOutVariables are the catalog's -w variables.
var writeOutVariables = map[string]bool{
	"http_code": true, "size_download": true, "url_effective": true, "time_total": true,
	"time_namelookup": true, "time_connect": true, "time_appconnect": true, "time_starttransfer": true,
	"remote_ip": true, "remote_port": true, "content_type": true, "http_version": true,
	"num_redirects": true, "redirect_url": true, "size_upload": true, "size_header": true,
	"speed_download": true, "exitcode": true,
}

// parseWriteOut accepts %{variable}, %% and the \n, \t, \r and \\ escapes.
func parseWriteOut(format string) ([]writeToken, error) {
	var tokens []writeToken
	var literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			tokens = append(tokens, writeToken{literal: literal.String()})
			literal.Reset()
		}
	}
	for i := 0; i < len(format); i++ {
		c := format[i]
		switch {
		case c == '%' && i+1 < len(format) && format[i+1] == '%':
			literal.WriteByte('%')
			i++
		case c == '%' && i+1 < len(format) && format[i+1] == '{':
			end := strings.IndexByte(format[i:], '}')
			if end < 0 {
				return nil, usage(format, "a closed %{variable}")
			}
			name := format[i+2 : i+end]
			if !writeOutVariables[name] {
				return nil, usage("%{"+name+"}", "a supported variable such as %{http_code}, %{time_total}, %{remote_ip} or %{exitcode}")
			}
			flush()
			tokens = append(tokens, writeToken{variable: name})
			i += end
		case c == '\\' && i+1 < len(format):
			switch format[i+1] {
			case 'n':
				literal.WriteByte('\n')
			case 't':
				literal.WriteByte('\t')
			case 'r':
				literal.WriteByte('\r')
			case '\\':
				literal.WriteByte('\\')
			default:
				literal.WriteByte(c)
				continue
			}
			i++
		default:
			literal.WriteByte(c)
		}
	}
	flush()
	return tokens, nil
}
