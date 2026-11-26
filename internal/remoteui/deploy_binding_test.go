package remoteui

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/targetregistry"
	"os"
	"path/filepath"
	"testing"
)

func TestDeploymentBindingMetadataContainsNoCredential(t *testing.T) {
	_, _, _, row, _ := credentialFixture(t)
	dir := filepath.Join(t.TempDir(), "registry")
	os.Mkdir(dir, 0700)
	if e := targetregistry.Init(dir); e != nil {
		t.Fatal(e)
	}
	r, e := targetregistry.OpenExisting(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Add(row, row); e != nil {
		t.Fatal(e)
	}
	metadata, e := DeploymentBindingMetadata(context.Background(), r, row.ID)
	if e != nil || metadata["binding"] != targetDigest(row) || len(metadata["enrollment"]) != 32 {
		t.Fatal("non-secret custody metadata unavailable", e)
	}
	body, _ := json.Marshal(metadata)
	if bytes.Contains(body, []byte(row.HTTPSPairing)) {
		t.Fatal("binding export leaked pairing credential")
	}
}
