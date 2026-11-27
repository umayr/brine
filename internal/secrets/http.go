package secrets

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"unicode/utf8"
)

func newHTTPClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = &http.Transport{
			// No proxies from the process environment. Verify against system
			// roots and the configured hostname; no insecure option.
			DialContext:            (&net.Dialer{Timeout: requestTimeout}).DialContext,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    requestTimeout,
			ResponseHeaderTimeout:  requestTimeout,
			MaxResponseHeaderBytes: 32 << 10,
			DisableCompression:     true,
			IdleConnTimeout:        requestTimeout,
			MaxIdleConns:           16,
		}
	}
	return &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func requestJSON(ctx context.Context, client *http.Client, method string, origin url.URL, route string, query url.Values, header, credential string, body []byte, dest any) error {
	origin.Path = route
	origin.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, origin.String(), reader)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if header != "" {
		req.Header.Set(header, credential)
	}
	res, err := client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrDenied
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrUnavailable
	default:
		if res.StatusCode >= 500 {
			return ErrUnavailable
		}
		return ErrInvalidResponse
	}
	if res.ContentLength > maxResponseBytes {
		return ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return ErrUnavailable
	}
	if len(data) > maxResponseBytes {
		return ErrTooLarge
	}
	if !utf8.Valid(data) || json.Unmarshal(data, dest) != nil {
		return ErrInvalidResponse
	}
	return nil
}

func readCredential(ctx context.Context, ref FileRef) (string, error) {
	data, err := readProtectedFile(ctx, ref, maxCredentialBytes)
	if err != nil {
		return "", err
	}
	// Accept one optional LF or CRLF, not whitespace or multiple lines.
	if bytes.HasSuffix(data, []byte("\r\n")) {
		data = data[:len(data)-2]
	} else {
		data = bytes.TrimSuffix(data, []byte("\n"))
	}
	credential := string(data)
	if !validCredential(credential) {
		return "", ErrDenied
	}
	return credential, nil
}

func validCredential(credential string) bool {
	if len(credential) == 0 || len(credential) > maxCredentialBytes {
		return false
	}
	for i := 0; i < len(credential); i++ {
		if credential[i] < '!' || credential[i] > '~' {
			return false
		}
	}
	return true
}

func stringValue(value string) (Value, error) {
	if len(value) > MaxValueBytes {
		return Value{}, ErrTooLarge
	}
	return newValue([]byte(value)), nil
}
