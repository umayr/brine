package secrets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

type vaultProvider struct {
	cfg    VaultConfig
	origin url.URL
	client *http.Client
}

func (vaultProvider) capabilities() Capabilities {
	return Capabilities{Kind: KindVault, VersionPin: true}
}

func (p vaultProvider) acquire(ctx context.Context, s Selector) (Value, error) {
	ref := s.Vault
	token, err := readCredential(ctx, p.cfg.TokenFile)
	if err != nil {
		return Value{}, err
	}

	query := make(url.Values)
	if ref.Revision != 0 {
		query.Set("version", strconv.FormatUint(ref.Revision, 10))
	}
	route := "/v1/" + p.cfg.Mount + "/data/" + p.cfg.PathPrefix + "/" + ref.Path
	var response struct {
		Data *struct {
			Fields   map[string]json.RawMessage `json:"data"`
			Metadata struct {
				Version      uint64 `json:"version"`
				Destroyed    bool   `json:"destroyed"`
				DeletionTime string `json:"deletion_time"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := requestJSON(ctx, p.client, http.MethodGet, p.origin, route, query, "X-Vault-Token", token, nil, &response); err != nil {
		return Value{}, err
	}
	if response.Data == nil || response.Data.Metadata.Version == 0 {
		return Value{}, ErrInvalidResponse
	}
	if ref.Revision != 0 && response.Data.Metadata.Version != ref.Revision {
		return Value{}, ErrVersionMismatch
	}
	// Vault returns the entire document. Only the selected string field is
	// exposed to the caller; neither the document nor its metadata is cached.
	if response.Data.Metadata.Destroyed || response.Data.Metadata.DeletionTime != "" {
		return Value{}, ErrNotFound
	}
	raw, ok := response.Data.Fields[ref.Key]
	if !ok {
		return Value{}, ErrNotFound
	}
	var value *string
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return Value{}, ErrInvalidResponse
	}
	v, err := stringValue(*value)
	v.revision = strconv.FormatUint(response.Data.Metadata.Version, 10)
	return v, err
}
