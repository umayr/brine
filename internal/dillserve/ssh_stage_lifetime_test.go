//go:build darwin || linux || freebsd || openbsd || netbsd

package dillserve

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSSHStageTunnelLoss(t *testing.T) {
	for _, mode := range []string{"exit", "close"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			processes := make(chan *fakeSSH, 1)
			start := func(args []string) (sshProcess, error) {
				var socket string
				for i, a := range args {
					if a == "-L" {
						socket = strings.Split(args[i+1], ":")[0]
					}
				}
				l, e := net.Listen("unix", socket)
				if e != nil {
					return nil, e
				}
				server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/health" {
						_, _ = w.Write([]byte(goodHealth))
						return
					}
					close(entered)
					<-r.Context().Done()
				})}
				p := &fakeSSH{done: make(chan struct{}), listener: l, server: server}
				processes <- p
				go server.Serve(l)
				return p, nil
			}
			tunnel, e := newSSH(context.Background(), options(t), start, time.Second)
			if e != nil {
				t.Fatal(e)
			}
			defer tunnel.Close()
			p := <-processes
			result := make(chan error, 1)
			go func() { _, e := tunnel.Stage(context.Background(), []byte("body")); result <- e }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request not received")
			}
			if mode == "exit" {
				_ = p.Kill()
			} else {
				tunnel.Close()
			}
			select {
			case e := <-result:
				if !errors.Is(e, ErrStageUnknown) {
					t.Fatalf("got %v", e)
				}
			case <-time.After(time.Second):
				t.Fatal("stage blocked")
			}
			if _, e = tunnel.Stage(context.Background(), []byte("body")); !errors.Is(e, ErrUnavailable) {
				t.Fatalf("closed tunnel: %v", e)
			}
		})
	}
}
