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

func TestSSHExitCancelsInFlightRequest(t *testing.T) {
	entered := make(chan struct{}, 1)
	processes := make(chan *fakeSSH, 1)
	start := func(args []string) (sshProcess, error) {
		var socket string
		for i, a := range args {
			if a == "-L" {
				socket = strings.Split(args[i+1], ":")[0]
			}
		}
		l, err := net.Listen("unix", socket)
		if err != nil {
			return nil, err
		}
		s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/health" {
				_, _ = w.Write([]byte(goodHealth))
				return
			}
			entered <- struct{}{}
			<-r.Context().Done()
		})}
		p := &fakeSSH{done: make(chan struct{}), listener: l, server: s}
		processes <- p
		go func() { _ = s.Serve(l) }()
		return p, nil
	}
	tunnel, err := newSSH(context.Background(), options(t), start, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	result := make(chan error, 1)
	go func() { _, e := tunnel.Status(context.Background()); result <- e }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	_ = (<-processes).Kill()
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request not cancelled")
	}
}
