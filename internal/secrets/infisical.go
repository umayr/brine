package secrets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type infisicalProvider struct {
	cfg    InfisicalConfig
	origin url.URL
	client *http.Client
}

func (infisicalProvider) capabilities() Capabilities {
	return Capabilities{Kind: KindInfisical, VersionPin: true}
}

func (p infisicalProvider) acquire(ctx context.Context, s Selector) (Value, error) {
	ref := s.Infisical
	folder := p.cfg.Folder
	if ref.Folder != "" {
		if folder != "/" && ref.Folder != folder && !strings.HasPrefix(ref.Folder, folder+"/") {
			return Value{}, ErrDenied
		}
		folder = ref.Folder
	}
	clientID, err := readCredential(ctx, p.cfg.ClientIDFile)
	if err != nil {
		return Value{}, err
	}
	clientSecret, err := readCredential(ctx, p.cfg.ClientSecretFile)
	if err != nil {
		return Value{}, err
	}
	payload, err := json.Marshal(struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}{clientID, clientSecret})
	if err != nil {
		return Value{}, ErrUnavailable
	}
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	if err := requestJSON(ctx, p.client, http.MethodPost, p.origin, "/api/v1/auth/universal-auth/login", nil, "", "", payload, &login); err != nil {
		return Value{}, err
	}
	if !validCredential(login.AccessToken) {
		return Value{}, ErrInvalidResponse
	}

	query := url.Values{
		"projectId":              {p.cfg.ProjectID},
		"environment":            {p.cfg.Environment},
		"secretPath":             {folder},
		"type":                   {"shared"},
		"includeImports":         {"false"},
		"expandSecretReferences": {"false"},
	}
	if ref.Revision != 0 {
		query.Set("version", strconv.FormatUint(ref.Revision, 10))
	}
	var response struct {
		Secret *struct {
			Key     string  `json:"secretKey"`
			Value   *string `json:"secretValue"`
			Version uint64  `json:"version"`
			Hidden  bool    `json:"secretValueHidden"`
		} `json:"secret"`
	}
	if err := requestJSON(ctx, p.client, http.MethodGet, p.origin, "/api/v4/secrets/"+ref.Key, query, "Authorization", "Bearer "+login.AccessToken, nil, &response); err != nil {
		return Value{}, err
	}
	if response.Secret == nil || response.Secret.Key != ref.Key || response.Secret.Version == 0 || response.Secret.Value == nil || response.Secret.Hidden {
		return Value{}, ErrInvalidResponse
	}
	if ref.Revision != 0 && response.Secret.Version != ref.Revision {
		return Value{}, ErrVersionMismatch
	}
	v, err := stringValue(*response.Secret.Value)
	v.revision = strconv.FormatUint(response.Secret.Version, 10)
	return v, err
}
