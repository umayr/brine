//go:build linux || darwin

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func useCredentialSession(t *testing.T, a *Authenticator) (*http.Request, string) {
	t.Helper()
	a.mu.Lock()
	login := a.login
	a.mu.Unlock()
	id, err := a.createSession(login)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/settings/general", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: id})
	sess := a.sessionFromRequest(r)
	if sess == nil {
		t.Fatal("no session")
	}
	return r.WithContext(withInfo(r.Context(), requestInfo{method: "cookie", user: sess.user, csrf: sess.csrf})), id
}

func TestManagedCredentialsPersistAndInvalidate(t *testing.T) {
	dir := credentialTestDir(t)
	a, _, _, err := NewWithCredentials(Config{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.CredentialFields() != (ManagedFields{true, true, true}) {
		t.Fatal("new file should manage all fields")
	}
	creds, _, _, err := LoadOrCreate(dir, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := useCredentialSession(t, a)
	other, _ := useCredentialSession(t, a)
	oldLogin := a.login
	ctx, cancel, ok := a.SessionContext(other)
	if !ok {
		t.Fatal("second session must be usable")
	}
	defer cancel()
	call := func(action CredentialAction, pw string) (CredentialResult, error) {
		return a.ManageCredentials(req, CredentialChange{Action: action, CurrentPassword: pw})
	}
	if got, err := call(ShowAPIToken, "wrong"); !errors.Is(err, ErrCredentialPassword) || got.Token != "" {
		t.Fatal("wrong password revealed the token")
	}
	got, err := call(ShowAPIToken, creds.Password)
	if err != nil || got.Token != creds.APIToken {
		t.Fatal("could not reveal the current API token", err)
	}
	rotated, err := call(RegenerateAPIToken, creds.Password)
	if err != nil || rotated.Token == "" || rotated.Token == creds.APIToken || a.sessionFromRequest(req) == nil || a.sessionFromRequest(other) == nil {
		t.Fatal("rotation failed or invalidated a session", err)
	}
	if _, ok := a.tokenIdentity(creds.APIToken); ok {
		t.Fatal("old API token still valid")
	}
	if _, ok := a.tokenIdentity(rotated.Token); !ok {
		t.Fatal("replacement API token is not valid")
	}
	username := "new-admin"
	password := "new example password"
	change := CredentialChange{Action: UpdateSignIn, Username: &username, CurrentPassword: creds.Password, NewPassword: password}
	got, err = a.ManageCredentials(req, change)
	if err != nil || !got.SignedOut || a.sessionFromRequest(req) != nil || a.sessionFromRequest(other) != nil {
		t.Fatal("sign-in update didn't revoke all sessions", err)
	}
	<-ctx.Done()
	if _, err := a.createSession(oldLogin); !errors.Is(err, errLoginChanged) {
		t.Fatal("in-flight authentication could create an old-login session", err)
	}
	if user, ok := a.tokenIdentity(rotated.Token); !ok || user != username {
		t.Fatal("sign-in update did not preserve the API token with its new identity")
	}
	if _, err := a.ManageCredentials(req, change); !errors.Is(err, ErrCredentialSession) {
		t.Fatal("revoked session still authorized a change")
	}
	restarted, _, _, err := NewWithCredentials(Config{}, dir)
	if err != nil || !restarted.login.verify(username, password) || restarted.login.verify(creds.User, creds.Password) {
		t.Fatal("new sign-in did not persist", err)
	}
	if _, ok := restarted.tokenIdentity(rotated.Token); !ok {
		t.Fatal("rotated token did not persist")
	}
}

func TestCredentialOwnershipAndUnavailableState(t *testing.T) {
	dir := credentialTestDir(t)
	base, _, path, err := LoadOrCreate(dir, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{User: "deployment-user", Password: "synthetic-deployment-password"}
	a, _, _, err := NewWithCredentials(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.CredentialFields() != (ManagedFields{APIToken: true}) {
		t.Fatal("explicit overrides must lock just their two fields")
	}
	req, _ := useCredentialSession(t, a)
	name := "attempted-change"
	_, err = a.ManageCredentials(req, CredentialChange{Action: UpdateSignIn, CurrentPassword: cfg.Password, Username: &name})
	if !errors.Is(err, ErrCredentialManaged) {
		t.Fatal("could change locked username", err)
	}
	got, err := a.ManageCredentials(req, CredentialChange{Action: RegenerateAPIToken, CurrentPassword: cfg.Password})
	if err != nil || got.Token == "" {
		t.Fatal("could not rotate independently managed token", err)
	}
	disk, _, _, err := LoadOrCreate(dir, "", "", "")
	if err != nil || disk.User != base.User || disk.Password != base.Password || disk.APIToken != got.Token {
		t.Fatal("rotation overwrote startup-only values in the file")
	}
	locked, _, _, err := NewWithCredentials(Config{APIToken: "synthetic-external-token"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if locked.CredentialFields() != (ManagedFields{Username: true, Password: true}) {
		t.Fatal("token override must lock just the token")
	}
	lockedReq, _ := useCredentialSession(t, locked)
	for _, action := range []CredentialAction{ShowAPIToken, RegenerateAPIToken} {
		if res, err := locked.ManageCredentials(lockedReq, CredentialChange{Action: action, CurrentPassword: base.Password}); !errors.Is(err, ErrCredentialManaged) || res.Token != "" {
			t.Fatal("external token became visible or mutable", err)
		}
	}
	external, _, file, err := NewWithCredentials(Config{Password: "external", APIToken: "external"}, dir)
	if err != nil || file != "" || external.CredentialFields() != (ManagedFields{}) {
		t.Fatal("explicit credentials should not use file management", err)
	}

	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".old", path); err != nil {
		t.Fatal(err)
	}
	for _, action := range []CredentialAction{ShowAPIToken, RegenerateAPIToken} {
		if res, err := a.ManageCredentials(req, CredentialChange{Action: action, CurrentPassword: cfg.Password}); !errors.Is(err, ErrCredentialUnavailable) || res.Token != "" {
			t.Fatal("unsafe file did not fail closed", err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ManageCredentials(req, CredentialChange{Action: RegenerateAPIToken, CurrentPassword: cfg.Password}); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatal("missing file did not fail closed", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("runtime credential change recreated a deleted file")
	}
	if _, ok := a.tokenIdentity(got.Token); !ok {
		t.Fatal("failed publish changed the active token")
	}
}

func TestCredentialReauthenticationSharesLoginRateLimit(t *testing.T) {
	a, _, _, err := NewWithCredentials(Config{}, credentialTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := useCredentialSession(t, a)
	for i := 0; i < maxAttempts; i++ {
		if !a.allowAttempt(clientIP(req)) {
			t.Fatal("could not fill shared budget")
		}
	}
	if _, err := a.ManageCredentials(req, CredentialChange{Action: ShowAPIToken, CurrentPassword: "wrong"}); !errors.Is(err, ErrCredentialRate) {
		t.Fatal("credential reauthentication bypassed the login budget", err)
	}
	ctx := withInfo(context.Background(), requestInfo{method: "bearer", user: "admin"})
	if _, err := a.ManageCredentials(req.WithContext(ctx), CredentialChange{Action: ShowAPIToken, CurrentPassword: strings.Repeat("x", 20)}); !errors.Is(err, ErrCredentialSession) {
		t.Fatal("bearer identity admitted to browser settings", err)
	}
}
