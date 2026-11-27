//go:build linux

package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/umayr/brine/internal/webhooks"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type brokerReceiver struct {
	mu                sync.Mutex
	key               []byte
	events            map[string]webhooks.Event
	failure           bool
	requests          int
	bodies            map[string][32]byte
	deliveries        map[string]string
	URL, CIDR, CAFile string
	server            *http.Server
}

func (r *brokerReceiver) setKey(key string) bool {
	if len(key) != 64 || strings.ToLower(key) != key {
		return false
	}
	if _, err := hex.DecodeString(key); err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.key = []byte(key)
	return true
}
func (r *brokerReceiver) verified() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure || len(r.events) != 2 || r.requests < 2 {
		return false
	}
	seen := map[string]int{}
	for _, event := range r.events {
		if event.Type != "operation.completed" || event.Outcome != "success" {
			return false
		}
		seen[event.Action]++
	}
	return seen["plan"] == 1 && seen["deploy"] == 1
}
func newBrokerReceiver(t *testing.T, root string, value []byte) *brokerReceiver {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var ip net.IP
	for _, a := range addresses {
		candidate, _, _ := net.ParseCIDR(a.String())
		if candidate.To4() != nil && candidate.IsPrivate() && !candidate.IsLoopback() {
			ip = candidate.To4()
			break
		}
	}
	if ip == nil {
		t.Fatal("private non-loopback receiver interface required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "disposable-broker-receiver"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{ip}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caPath := filepath.Join(root, "receiver-ca.pem")
	if err = os.WriteFile(caPath, ca, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	receiver := &brokerReceiver{bodies: map[string][32]byte{}, deliveries: map[string]string{}, events: map[string]webhooks.Event{}, URL: "https://" + listener.Addr().String() + "/notify", CIDR: ip.String() + "/32", CAFile: caPath}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, readErr := io.ReadAll(io.LimitReader(req.Body, 65537))
		defer req.Body.Close()
		receiver.mu.Lock()
		defer receiver.mu.Unlock()
		mac := hmac.New(sha256.New, receiver.key)
		mac.Write([]byte(req.Header.Get("Brine-Timestamp") + "."))
		mac.Write(body)
		signature, e := hex.DecodeString(strings.TrimPrefix(req.Header.Get("Brine-Signature"), "v1="))
		stamp, se := strconv.ParseInt(req.Header.Get("Brine-Timestamp"), 10, 64)
		receiver.requests++
		var event webhooks.Event
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if req.Method != "POST" || req.URL.Path != "/notify" || readErr != nil || len(body) > 65536 || len(receiver.key) != 64 || e != nil || se != nil || time.Since(time.Unix(stamp, 0)).Abs() > 5*time.Minute || !hmac.Equal(signature, mac.Sum(nil)) || !strings.HasPrefix(req.Header.Get("Brine-Signature"), "v1=") || bytes.Contains(body, value) || bytes.Contains(body, []byte("runtime-files")) || bytes.Contains(body, []byte("generation")) || decoder.Decode(&event) != nil || req.Header.Get("Brine-Event-ID") != event.ID || req.Header.Get("Brine-Delivery-ID") == "" {
			receiver.failure = true
			http.Error(w, "verification failed", 400)
			return
		}
		digest := sha256.Sum256(body)
		if previous, ok := receiver.bodies[event.ID]; ok && (previous != digest || receiver.deliveries[event.ID] != req.Header.Get("Brine-Delivery-ID")) {
			receiver.failure = true
			http.Error(w, "unstable retry", 400)
			return
		}
		// At-least-once retries are allowed but every request must verify and
		// preserve raw bytes plus event/delivery identity, never hide a new event.
		receiver.bodies[event.ID] = digest
		receiver.deliveries[event.ID] = req.Header.Get("Brine-Delivery-ID")
		receiver.events[event.ID] = event
		w.WriteHeader(204)
	})
	receiver.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		e := receiver.server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}))
		if e != nil && e != http.ErrServerClosed {
			receiver.mu.Lock()
			receiver.failure = true
			receiver.mu.Unlock()
		}
	}()
	t.Cleanup(func() { receiver.server.Close(); receiver.mu.Lock(); clear(receiver.key); receiver.mu.Unlock() })
	return receiver
}
func (r *brokerReceiver) wait() error {
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		if r.verified() {
			return nil
		}
		time.Sleep(40 * time.Millisecond)
	}
	return fmt.Errorf("signed production broker notifications absent or invalid")
}

func (r *brokerReceiver) matches(expected webhooks.Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	actual, ok := r.events[expected.ID]
	if !ok {
		return false
	}
	a, err := json.Marshal(actual)
	if err != nil {
		return false
	}
	b, err := json.Marshal(expected)
	return err == nil && bytes.Equal(a, b)
}
