package engine

import (
	"context"
	"fmt"
	"github.com/docker/docker/client"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInspectionHealthUsesStructuredFieldsNotStatusText(t *testing.T) {
	for _, tc := range []struct {
		name, config, state string
		configured          bool
		reported            bool
	}{{"healthy", `{"Image":"fixture","Labels":{},"Healthcheck":{"Test":["CMD","true"]}}`, `{"Status":"running","Health":{"Status":"healthy"}}`, true, true}, {"unchecked", `{"Image":"fixture","Labels":{}}`, `{"Status":"running"}`, false, false}, {"missing", `{"Image":"fixture","Labels":{},"Healthcheck":{"Test":["CMD","true"]}}`, `{"Status":"running"}`, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("inspection attempted mutation")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"Id":"fixture","Name":"/fixture","Config":%s,"State":%s,"NetworkSettings":{"Ports":{}}}`, tc.config, tc.state)
			}))
			defer server.Close()
			cli, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.45"))
			if e != nil {
				t.Fatal(e)
			}
			defer cli.Close()
			engine := &DockerEngine{cli: cli}
			info, e := engine.InspectContainer(context.Background(), "fixture")
			if e != nil {
				t.Fatal(e)
			}
			if info.HealthConfigured == nil || *info.HealthConfigured != tc.configured || (info.HealthStatus != nil) != tc.reported {
				t.Fatal("unavailable and unchecked health conflated", info)
			}
			if tc.reported && *info.HealthStatus != "healthy" {
				t.Fatal("raw health status lost")
			}
		})
	}
}
