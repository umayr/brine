package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A private fixture relay uses the actual target leaf and exact upstream pin.
// It never invents or edits a target result: only one already-received stop
// response is discarded to model loss across the real HTTPS request boundary.
type responseLossRelay struct {
	Phase    atomic.Int32
	Arm      atomic.Bool
	Stops    atomic.Int32
	Restarts atomic.Int32
	Dropped  atomic.Bool
	Invalid  atomic.Bool
}

func newResponseLossRelay(t *testing.T, credentials, address string) (string, *responseLossRelay) {
	t.Helper()
	cert, e := tls.LoadX509KeyPair(filepath.Join(credentials, "tls-cert.pem"), filepath.Join(credentials, "tls-key.pem"))
	if e != nil {
		t.Fatal(e)
	}
	state := &responseLossRelay{}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(s tls.ConnectionState) error {
		if len(s.PeerCertificates) == 0 || !bytes.Equal(s.PeerCertificates[0].Raw, cert.Certificate[0]) {
			return errors.New("fixture upstream pin mismatch")
		}
		return nil
	}}}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/deploy/stop" {
			state.Stops.Add(1)
		}
		if r.URL.Path == "/v1/deploy/restart" {
			state.Restarts.Add(1)
		}
		target := url.URL{Scheme: "https", Host: address, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
		req, e := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
		if e != nil {
			http.Error(w, "fixture transport unavailable", 502)
			return
		}
		req.Header = r.Header.Clone()
		response, e := client.Do(req)
		if e != nil {
			http.Error(w, "fixture transport unavailable", 502)
			return
		}
		defer response.Body.Close()
		body, e := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if e != nil {
			http.Error(w, "fixture response unavailable", 502)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v1/deploy/stop" && state.Arm.CompareAndSwap(true, false) {
			var status struct{ State string }
			if response.StatusCode != 200 || json.Unmarshal(body, &status) != nil || status.State != "succeeded" {
				state.Invalid.Store(true)
			}
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				state.Invalid.Store(true)
				http.Error(w, "fixture disconnect unavailable", 502)
				return
			}
			if e = conn.Close(); e != nil {
				state.Invalid.Store(true)
				return
			}
			state.Dropped.Store(true)
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}
	server.StartTLS()
	t.Cleanup(func() { server.Close(); transport.CloseIdleConnections() })
	addr := server.Listener.Addr().String()
	if host, _, e := net.SplitHostPort(addr); e != nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("relay must remain loopback")
	}
	return addr, state
}
