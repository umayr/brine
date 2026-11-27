package remoteui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/umayr/brine/internal/targetregistry"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func credentialFixture(t *testing.T) (string, string, string, targetregistry.Record, targetregistry.HistoryIncarnation) {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	data := filepath.Join(root, "data")
	private := filepath.Join(root, "credentials")
	for _, p := range []string{data, private} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	monitor := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	row := targetregistry.Record{ID: "target", Revision: 1, HTTPSAddress: "127.0.0.1:9443", HTTPSPairing: targetregistry.PairingSecret(fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":%q,"engineId":"11111111-1111-4111-8111-111111111111","generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"synthetic"}`, strings.Repeat("11", 32), monitor, strings.Repeat("33", 32)))}
	var scope targetregistry.HistoryIncarnation
	scope[0] = 7
	tokenPath := filepath.Join(private, "token")
	os.WriteFile(tokenPath, []byte(token), 0600)
	index := filepath.Join(private, "index.json")
	doc := map[string]any{"version": 1, "targets": map[string]any{row.ID: deploymentCredentialEntry{Binding: targetDigest(row), Enrollment: hex.EncodeToString(scope[:]), TokenFile: tokenPath}}}
	body, _ := json.Marshal(doc)
	if e := os.WriteFile(index, body, 0600); e != nil {
		t.Fatal(e)
	}
	return data, index, tokenPath, row, scope
}
func TestDeploymentCredentialCustodyBindsEnrollmentAndTarget(t *testing.T) {
	data, index, token, row, scope := credentialFixture(t)
	raw, readErr := privateDeploymentFile(index, data, 65536)
	if readErr != nil {
		t.Fatal("private index read rejected", readErr)
	}
	if !uniqueCredentialJSON(raw) {
		t.Fatal("unique JSON rejected")
	}
	source, e := LoadDeploymentCredentials(index, data)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.WithValue(context.Background(), deploymentEnrollmentKey{}, scope)
	if _, e = source.Credential(ctx, row); e != nil {
		t.Fatal(e)
	}
	other := scope
	other[0]++
	if _, e = source.Credential(context.WithValue(ctx, deploymentEnrollmentKey{}, other), row); e == nil {
		t.Fatal("forgotten enrollment reused authority")
	}
	changed := row
	changed.HTTPSAddress = "127.0.0.2:9443"
	if _, e = source.Credential(ctx, changed); e == nil {
		t.Fatal("changed target reused authority")
	}
	if e = os.Chmod(token, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = source.Credential(ctx, row); e == nil {
		t.Fatal("unsafe token accepted")
	}
	os.Chmod(token, 0600)
	os.Remove(index)
	if _, e = source.Credential(ctx, row); e == nil {
		t.Fatal("missing index enabled cached authority")
	}
}
func TestDeploymentCredentialIndexFailsClosed(t *testing.T) {
	data, index, token, _, _ := credentialFixture(t)
	body, _ := os.ReadFile(index)
	if e := os.WriteFile(index, append(body, []byte(`{}`)...), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadDeploymentCredentials(index, data); e == nil {
		t.Fatal("trailing index accepted")
	}
	os.WriteFile(index, []byte(`{"version":1,"version":1,"targets":{}}`), 0600)
	if _, e := LoadDeploymentCredentials(index, data); e == nil {
		t.Fatal("duplicate index keys accepted")
	}
	if e := os.Link(token, filepath.Join(filepath.Dir(token), "alias")); e != nil {
		t.Fatal(e)
	}
	if _, e := privateDeploymentFile(token, data, 44); e == nil {
		t.Fatal("hardlinked token accepted")
	}
	inside := filepath.Join(data, "key")
	os.WriteFile(inside, []byte("fixture"), 0600)
	if _, e := privateDeploymentFile(inside, data, 44); e == nil {
		t.Fatal("source/data credential accepted")
	}
}

func TestDeploymentCredentialPathsRejectMutableAliases(t *testing.T) {
	data, index, token, _, _ := credentialFixture(t)
	for _, original := range []string{index, token} {
		alias := original + ".alias"
		if e := os.Symlink(original, alias); e != nil {
			t.Fatal(e)
		}
		if _, e := privateDeploymentFile(alias, data, 65536); e == nil {
			t.Fatal("file symlink accepted")
		}
	}
	aliasDir := filepath.Join(filepath.Dir(data), "alias-dir")
	if e := os.Symlink(filepath.Dir(index), aliasDir); e != nil {
		t.Fatal(e)
	}
	if _, e := privateDeploymentFile(filepath.Join(aliasDir, filepath.Base(index)), data, 65536); e == nil {
		t.Fatal("parent alias accepted")
	}
}
