package webhooks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in disposable Linux network check: no live receiver or engine required.
func TestRealHTTPSDeliveryPreservesUnknownAndHistory(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("BRINE_REAL_WEBHOOK_HTTPS") != "1" {
		t.Skip("explicit disposable Linux HTTPS qualification required")
	}
	addresses, e := net.InterfaceAddrs()
	if e != nil {
		t.Fatal(e)
	}
	var ip net.IP
	for _, address := range addresses {
		candidate, _, err := net.ParseCIDR(address.String())
		if err == nil && candidate.To4() != nil && candidate.IsPrivate() && !candidate.IsLoopback() {
			ip = candidate.To4()
			break
		}
	}
	if ip == nil {
		t.Fatal("no private non-loopback receiver interface")
	}
	private, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{ip}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	var signingKey atomic.Value
	receipts := make(chan string, 4)
	receiver := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
		key, ready := signingKey.Load().(string)
		if !ready || err != nil || r.TLS == nil || Verify([]byte(key), r.Header.Get("Brine-Timestamp"), r.Header.Get("Brine-Signature"), body, time.Now()) != nil {
			t.Error("invalid authenticated TLS delivery")
			w.WriteHeader(403)
			return
		}
		var event Event
		if json.Unmarshal(body, &event) != nil || event.ID != "evt_real_https" || event.Outcome != "unknown" || r.Header.Get("Brine-Event-ID") != event.ID || r.Header.Get("Brine-Delivery-ID") == "" {
			t.Error("receiver lost exact event identity or raw outcome")
			w.WriteHeader(400)
			return
		}
		select {
		case receipts <- r.Header.Get("Brine-Delivery-ID"):
		default:
			t.Error("unexpected delivery overflow")
		}
		w.WriteHeader(200)
		io.WriteString(w, "synthetic receiver body must not be retained")
	}))
	listener, e := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if e != nil {
		t.Fatal(e)
	}
	receiver.Listener.Close()
	receiver.Listener = listener
	receiver.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}}}
	receiver.Config.ErrorLog = log.New(io.Discard, "", 0)
	receiver.StartTLS()
	t.Cleanup(receiver.Close)
	ctx := context.Background()
	encryptionKey := make([]byte, 32)
	if _, e = rand.Read(encryptionKey); e != nil {
		t.Fatal(e)
	}
	s, e := Open(Config{Directory: filepath.Join(t.TempDir(), "private"), EncryptionKey: encryptionKey, MaxPending: 10, AllowPrivateCIDRs: []string{ip.String() + "/32"}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	endpoint, key, e := s.SaveEndpoint(ctx, Endpoint{Name: "Disposable HTTPS", URL: receiver.URL, Events: []string{"operation.completed"}, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	signingKey.Store(key)
	client := s.client // production Open builds the deny-by-default transport
	var certificateRejected atomic.Bool
	productionTransport := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := productionTransport.RoundTrip(r) // observe only; preserve real policy and TLS
		var untrusted x509.UnknownAuthorityError
		if errors.As(err, &untrusted) {
			certificateRejected.Store(true)
		}
		return response, err
	})
	if e = s.Publish(ctx, testEvent("evt_real_https")); e != nil {
		t.Fatal(e)
	}
	if sent, e := s.sendOne(ctx); e != nil || !sent {
		t.Fatal("untrusted attempt not recorded", e)
	}
	rows, e := s.Deliveries(ctx, endpoint.ID)
	if e != nil || len(rows) != 1 || rows[0].Status != "pending" || rows[0].ErrorCategory != "network_unavailable" || calls.Load() != 0 || !certificateRejected.Load() {
		t.Fatal("untrusted TLS reached receiver or changed delivery semantics", e)
	}
	deliveryID := rows[0].ID
	// Trust only this generated fixture certificate; retain hostname/IP checks,
	// validated numeric dialing, no ambient proxy and no redirects.
	trusted, e := NewDeliveryClient([]string{ip.String() + "/32"})
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	trusted.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	productionTransport.(*http.Transport).CloseIdleConnections()
	defer trusted.CloseIdleConnections()
	s.client = trusted
	if _, e = s.db.Exec("UPDATE deliveries SET next_at=0 WHERE id=?", deliveryID); e != nil {
		t.Fatal(e)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Run(workerCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(12 * time.Second):
			t.Error("sender did not stop")
		}
	})
	wait := func(attempts int) {
		t.Helper()
		end := time.Now().Add(15 * time.Second)
		for time.Now().Before(end) {
			rows, e = s.Deliveries(ctx, endpoint.ID)
			if e == nil && len(rows) == 1 && rows[0].Status == "delivered" && rows[0].Attempts == attempts {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("real sender did not deliver", attempts, e)
	}
	wait(2)
	if e = s.Redeliver(ctx, deliveryID); e != nil {
		t.Fatal(e)
	}
	wait(3)
	if calls.Load() != 2 || len(receipts) != 2 {
		t.Fatal("receiver count differs", calls.Load())
	}
	for i := 0; i < 2; i++ {
		if <-receipts != deliveryID {
			t.Fatal("redelivery lost stable delivery identity")
		}
	}
	attempts, e := s.Attempts(ctx, deliveryID)
	if e != nil || len(attempts) != 3 || attempts[0].HTTPStatus != 0 || attempts[1].HTTPStatus != 200 || attempts[2].HTTPStatus != 200 {
		t.Fatal("durable attempts differ", e, attempts)
	}
	raw, e := json.Marshal(attempts)
	if e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"deliveries", "attempts", "events"} {
		stored, e := s.db.QueryContext(ctx, "SELECT * FROM "+table)
		if e != nil {
			t.Fatal(e)
		}
		columns, e := stored.Columns()
		if e != nil {
			stored.Close()
			t.Fatal(e)
		}
		for stored.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if e = stored.Scan(pointers...); e != nil {
				stored.Close()
				t.Fatal(e)
			}
			for _, value := range values {
				var text string
				switch v := value.(type) {
				case string:
					text = v
				case []byte:
					text = string(v)
				}
				if strings.Contains(text, "synthetic receiver body") {
					stored.Close()
					t.Fatal("receiver body persisted")
				}
			}
		}
		if e = stored.Err(); e != nil {
			stored.Close()
			t.Fatal(e)
		}
		stored.Close()
	}
	if strings.Contains(string(raw), "synthetic receiver body") || rows[0].Event.Outcome != "unknown" {
		t.Fatal("receiver changed outcome or body retained")
	}
	t.Log("real TLS rejected unknown certificate, production sender verified HMAC delivery/redelivery, raw unknown and three attempts preserved")
}
