package remoteui

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestRestartMetadataQueryIsSelectorOnly(t *testing.T) {
	for _, query := range []string{"host=demo", "host=demo&after=opaque"} {
		if _, err := parseRestartMetadataQuery(httptest.NewRequest("GET", "/?"+query, nil)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"", "host=demo&host=other", "host=demo&scope=foreign", "host=demo&approval=grant", "host=demo&after=a&after=b", "host=demo&after=%zz", "host=demo;scope=other"} {
		t.Run(query, func(t *testing.T) {
			if _, err := parseRestartMetadataQuery(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
				t.Fatal("accepted ambiguous or authority-bearing query")
			}
		})
	}
}

func TestRestartScopeAuthorizerRechecksExactScope(t *testing.T) {
	calls := 0
	denied := errors.New("revoked")
	var result error
	a := restartScopeAuthorizer{scope: "enrollment", check: func(context.Context) error { calls++; return result }}
	if a.AuthorizeSnapshotHistoryRestart(context.Background(), []byte("foreign")) == nil || calls != 0 {
		t.Fatal("foreign scope authorized")
	}
	if err := a.AuthorizeSnapshotHistoryRestart(context.Background(), []byte("enrollment")); err != nil || calls != 1 {
		t.Fatal("current approval refused")
	}
	result = denied
	if !errors.Is(a.AuthorizeSnapshotHistoryRestart(context.Background(), []byte("enrollment")), denied) || calls != 2 {
		t.Fatal("revocation not checked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a.AuthorizeSnapshotHistoryRestart(ctx, []byte("enrollment")) == nil || calls != 2 {
		t.Fatal("cancelled request authorized")
	}
}

func TestRestartMetadataMissingAuthenticatorDenies(t *testing.T) {
	var h *Deployment
	w := httptest.NewRecorder()
	h.protectedRestartMetadata(nil, nil).ServeHTTP(w, httptest.NewRequest("GET", "/?host=demo", nil))
	if w.Code != 503 {
		t.Fatalf("status %d", w.Code)
	}
}
