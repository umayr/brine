//go:build linux

package brokerapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
)

type runtimeStager interface {
	Stage(context.Context, []byte) (string, error)
	Snapshot(context.Context, string) (dillserve.Manifest, error)
}

func TestBrokerRuntimeHTTPSFile(t *testing.T) {
	if os.Getenv("BRINE_BROKER_RUNTIME_TEST") != "1" {
		t.Skip("explicit sandbox grant required")
	}
	t.Setenv("BRINE_BROKER_RUNTIME_TRANSPORT", "https")
	TestBrokerRuntimeLocalFile(t)
}

func TestBrokerRuntimeHTTPSVault(t *testing.T) {
	if os.Getenv("BRINE_BROKER_RUNTIME_TEST") != "1" {
		t.Skip("explicit sandbox grant required")
	}
	t.Setenv("BRINE_BROKER_RUNTIME_TRANSPORT", "https")
	t.Setenv("BRINE_BROKER_RUNTIME_PROVIDER", "vault")
	TestBrokerRuntimeLocalFile(t)
}

func runtimeHTTPSTargetPolicy(t *testing.T, ctx context.Context, fixture string, c dill.LocalBrokerConfig) TargetPolicy {
	t.Helper()
	credentials := filepath.Join(fixture, "https-credentials")
	if err := exec.CommandContext(ctx, c.Binary, "agent", "init-https", "--dir", credentials).Run(); err != nil {
		t.Fatal("private HTTPS provisioning", err)
	}
	if err := os.Mkdir(c.HistoryDir, 0700); err != nil {
		t.Fatal(err)
	}
	deploy := make([]byte, 32)
	if _, err := rand.Read(deploy); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(fixture, "https-deploy-token")
	if os.WriteFile(tokenFile, []byte(base64.RawURLEncoding.EncodeToString(deploy)), 0600) != nil {
		t.Fatal("private deploy token write")
	}
	clear(deploy)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	engineID := "11111111-1111-4111-8111-111111111111"
	monitorSocket, monitorDaemon := c.Socket, c.DaemonID
	if c.ProfilePath != "" {
		if c.Engine != "docker" {
			t.Fatal("HTTPS agent monitor is Docker-only")
		}
		var profile struct {
			Docker *struct{ Socket, DaemonID string } `json:"docker"`
		}
		raw, e := os.ReadFile(c.ProfilePath)
		if e != nil || json.Unmarshal(raw, &profile) != nil || profile.Docker == nil {
			t.Fatal("independent operator monitor metadata unavailable")
		}
		monitorSocket, monitorDaemon = profile.Docker.Socket, profile.Docker.DaemonID
	}
	args := []string{"agent", "--https-dir", credentials, "--https-listen", address, "--engine-socket", monitorSocket, "--engine-id", engineID, "--daemon-id", monitorDaemon, "--generation", "1", "--https-deploy-token-file", tokenFile, "--https-staging-root", c.StagingRoot, "--https-staging-catalog", c.CatalogRoot, "--https-preview-state", c.CoordinationDir, "--https-preview-pkl", c.Pkl, "--https-apply-state", c.HistoryDir, "--https-deploy-grant", "stage", "--https-deploy-grant", "manifest"}
	start := func(argv []string, label string) func() {
		t.Helper()
		lifetime, cancel := context.WithCancel(ctx)
		cmd := exec.CommandContext(lifetime, c.Binary, argv...)
		logFile, e := os.OpenFile(filepath.Join(fixture, label+".log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if e != nil {
			t.Fatal(e)
		}
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if e = cmd.Start(); e != nil {
			cancel()
			logFile.Close()
			t.Fatal("HTTPS child start", e)
		}
		stop := func() { cancel(); _ = cmd.Wait(); _ = logFile.Close() }
		ready := false
		for n := 0; n < 100; n++ {
			conn, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if e == nil {
				conn.Close()
				ready = true
				break
			}
			select {
			case <-ctx.Done():
				stop()
				t.Fatal("HTTPS startup canceled")
			case <-time.After(50 * time.Millisecond):
			}
		}
		if !ready {
			stop()
			t.Fatal("HTTPS listener unavailable; private child log retained")
		}
		return stop
	}
	// Fresh, explicit history provisioning through the actual agent constructor;
	// no mutation request is exposed/sent during bootstrap and no used state reset.
	stop := start(args, "https-bootstrap")
	stop()
	args = append(args, "--https-broker-store", c.RuntimeDir)
	if c.ProfilePath != "" {
		args = append(args, "--https-broker-profile", c.ProfilePath)
	}
	for _, grant := range []string{"broker-requirements", "broker-prepare", "broker-retire", "broker-review", "broker-apply", "broker-status", "fence", "status"} {
		args = append(args, "--https-deploy-grant", grant)
	}
	stop = start(args, "https-agent")
	t.Cleanup(stop)
	exportArgs := append(append([]string{}, args...), "--print-https-pairing")
	raw, e := exec.CommandContext(ctx, c.Binary, exportArgs...).Output()
	if e != nil {
		t.Fatal("private pairing export", e)
	}
	defer clear(raw)
	pairingFile := filepath.Join(fixture, "https-pairing.json")
	if os.WriteFile(pairingFile, raw, 0600) != nil {
		t.Fatal("private pairing write")
	}
	if _, e = agentclient.ParsePairing(raw); e != nil {
		t.Fatal("actual agent pairing rejected", e)
	}
	var bundle struct {
		Certificate string `json:"certificateSha256"`
		Key         string `json:"agentPublicKey"`
		ID          string `json:"engineId"`
		Generation  uint64 `json:"generation"`
		Kind        string `json:"engineKind"`
		Socket      string `json:"engineSocket"`
		Daemon      string `json:"daemonId"`
	}
	if json.Unmarshal(raw, &bundle) != nil {
		t.Fatal("pairing metadata unavailable")
	}
	approved := agentclient.OperatorTarget{Address: address, LeafSHA256: bundle.Certificate, AgentPublicKey: bundle.Key, EngineID: engineID, EngineKind: "docker", EngineSocket: "unix://" + monitorSocket, DaemonID: monitorDaemon, Generation: 1}
	t.Logf("real HTTPS agent ready on loopback; pinned leaf and separate monitor/deploy authority")
	return TargetPolicy{Kind: "https", HTTPS: &HTTPSTargetPolicy{PairingFile: pairingFile, DeployTokenFile: tokenFile, Approved: approved, BrokerTargetDigest: c.TargetDigest}, Grants: []string{"stage", "manifest", "requirements", "prepare", "retire", "review", "apply", "status", "fence"}}
}
