package hostproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

// Failure reports transport state without retaining proxy credentials or diagnostics.
type Failure string

const (
	PolicyUnavailable Failure = "the configured proxy policy could not be resolved"
	EndpointInvalid   Failure = "the configured proxy endpoint is unsupported"
	ProxyUnavailable  Failure = "the configured proxy could not be reached"
	ProxyRejected     Failure = "the configured proxy refused the connection or authentication"
	ProxyTLS          Failure = "the configured HTTPS proxy certificate or TLS handshake could not be verified"
	ProxyProtocol     Failure = "the configured proxy returned an invalid or oversized response"
)

func (failure Failure) Error() string { return "host proxy: " + string(failure) }

// DialPinned applies policy for target but tunnels only the address the caller
// already authorized. Proxy discovery never resolves or grants a destination.
func DialPinned(ctx context.Context, target *url.URL, network string, address netip.AddrPort) (net.Conn, error) {
	if target == nil || target.Hostname() == "" || !address.IsValid() || address.Port() == 0 || network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, EndpointInvalid
	}
	selected, err := Resolve(target)
	if err != nil {
		return nil, PolicyUnavailable
	}
	if selected == nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, address.String())
	}
	return tunnel(ctx, selected, network, address)
}

func tunnel(ctx context.Context, selected *url.URL, network string, address netip.AddrPort) (net.Conn, error) {
	endpoint, err := proxyAddress(selected)
	if err != nil {
		return nil, err
	}
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(setup, network, endpoint)
	if err != nil {
		return nil, ProxyUnavailable
	}
	rawConnection := connection
	stop := context.AfterFunc(setup, func() { _ = rawConnection.Close() })
	defer stop()
	complete := false
	defer func() {
		if !complete {
			_ = rawConnection.Close()
		}
	}()
	deadline, _ := setup.Deadline()
	if connection.SetDeadline(deadline) != nil {
		return nil, ProxyUnavailable
	}
	if selected.Scheme == "https" {
		secure := tls.Client(connection, &tls.Config{ServerName: selected.Hostname(), MinVersion: tls.VersionTLS12})
		if secure.HandshakeContext(setup) != nil {
			return nil, ProxyTLS
		}
		connection = secure
	}
	if selected.Scheme == "socks5" || selected.Scheme == "socks5h" {
		err = socks(connection, selected.User, address)
	} else {
		connection, err = connect(connection, selected.User, address)
	}
	if err != nil {
		return nil, err
	}
	if !stop() || setup.Err() != nil {
		return nil, ProxyUnavailable
	}
	if connection.SetDeadline(time.Time{}) != nil {
		return nil, ProxyUnavailable
	}
	complete = true
	return connection, nil
}

func proxyAddress(selected *url.URL) (string, error) {
	if selected == nil || selected.Hostname() == "" || selected.RawQuery != "" || selected.Fragment != "" || selected.Path != "" && selected.Path != "/" {
		return "", EndpointInvalid
	}
	port := selected.Port()
	switch selected.Scheme {
	case "http":
		if port == "" {
			port = "80"
		}
	case "https":
		if port == "" {
			port = "443"
		}
	case "socks5", "socks5h":
		if port == "" {
			port = "1080"
		}
	default:
		return "", EndpointInvalid
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 {
		return "", EndpointInvalid
	}
	return net.JoinHostPort(selected.Hostname(), port), nil
}

type bufferedConnection struct {
	net.Conn
	reader *bufio.Reader
}

func (connection *bufferedConnection) Read(value []byte) (int, error) {
	return connection.reader.Read(value)
}

func connect(connection net.Conn, credentials *url.Userinfo, address netip.AddrPort) (net.Conn, error) {
	request := "CONNECT " + address.String() + " HTTP/1.1\r\nHost: " + address.String() + "\r\n"
	if credentials != nil {
		password, _ := credentials.Password()
		if len(credentials.Username())+len(password) > 8192 {
			return nil, EndpointInvalid
		}
		token := base64.StdEncoding.EncodeToString([]byte(credentials.Username() + ":" + password))
		request += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	if _, err := io.WriteString(connection, request+"\r\n"); err != nil {
		return nil, ProxyUnavailable
	}
	reader := bufio.NewReaderSize(connection, 4096)
	var header []byte
	for {
		line, err := reader.ReadSlice('\n')
		if len(header)+len(line) > 16<<10 {
			return nil, ProxyProtocol
		}
		header = append(header, line...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return nil, ProxyProtocol
		}
		if bytes.HasSuffix(header, []byte("\r\n\r\n")) {
			break
		}
	}
	reply, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header)), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, ProxyProtocol
	}
	if reply.StatusCode != http.StatusOK {
		return nil, ProxyRejected
	}
	return &bufferedConnection{Conn: connection, reader: reader}, nil
}
