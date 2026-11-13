//go:build unix

package git

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

func TestTargetBackgroundHelper(t *testing.T) {
	sep := -1
	for i, a := range os.Args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return
	}
	args := os.Args[sep+1:]
	socket := args[0]
	for i, a := range args {
		if a == "--engine-socket" && i+1 < len(args) {
			socket = args[i+1]
		}
	}
	c, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		os.Exit(2)
	}
	_, err = io.WriteString(c, strings.Join(args[1:], " "))
	_ = c.Close()
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestTwoEndpointChangedPullAndAlphaBackgroundBoundary(t *testing.T) {
	for _, profile := range []string{"legacy", "alpha"} {
		for _, kind := range []string{"docker", "podman"} {
			t.Run(profile+"/"+kind, func(t *testing.T) {
				root, err := os.MkdirTemp("/tmp", "brine-bg-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(root) })
				sockets := [2]string{filepath.Join(root, "wrong.sock"), filepath.Join(root, "selected.sock")}
				var mu sync.Mutex
				calls := [2][]string{}
				received := make(chan struct{}, 2)
				var workers sync.WaitGroup
				for i, p := range sockets {
					l, e := net.Listen("unix", p)
					if e != nil {
						t.Fatal(e)
					}
					idx := i
					workers.Add(1)
					go func() {
						defer workers.Done()
						for {
							c, e := l.Accept()
							if e != nil {
								return
							}
							b, _ := io.ReadAll(c)
							_ = c.Close()
							mu.Lock()
							calls[idx] = append(calls[idx], string(b))
							mu.Unlock()
							received <- struct{}{}
						}
					}()
					t.Cleanup(func() { _ = l.Close() })
				}
				t.Setenv("DOCKER_HOST", "unix://"+sockets[0])
				t.Setenv("PODMAN_SOCKET", sockets[0])
				st, err := store.NewFSStore(filepath.Join(root, "workspace"))
				if err != nil {
					t.Fatal(err)
				}
				repo, err := st.RepoDir("demo")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(repo, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(repo, "compose.pkl"), []byte(`config { engine = "`+kind+`" }`), 0600); err != nil {
					t.Fatal(err)
				}
				stack, err := st.CreateFromGit("demo", &store.GitSource{URL: "synthetic", AutoDeploy: true}, "")
				if err != nil {
					t.Fatal(err)
				}
				counter, trace := filepath.Join(root, "counter"), filepath.Join(root, "trace")
				gitBin := filepath.Join(root, "git")
				script := "#!/bin/sh\nwhile [ \"$1\" = -c ]; do shift 2; done\ncase \"$1 $2\" in\n'status --porcelain') exit 0;;\n'pull --ff-only') echo pull >> '" + trace + "';;\n'rev-parse HEAD') if [ -e '" + counter + "' ]; then echo new; echo new >> '" + trace + "'; else echo old; echo old >> '" + trace + "'; touch '" + counter + "'; fi;;\n'rev-parse --short') echo new;;\n*) exit 7;;\nesac\n"
				if err = os.WriteFile(gitBin, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				testBin, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				dillBin := filepath.Join(root, "dill")
				if err = os.WriteFile(dillBin, []byte("#!/bin/sh\nexec '"+testBin+"' -test.run='^TestTargetBackgroundHelper$' -- '"+sockets[0]+"' \"$@\"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				r := dill.New(dillBin)
				r.Profile = profile
				r.Engine = kind
				r.Socket = sockets[1]
				r.ResolveStack = func(p string) (string, error) {
					if p != stack.PklPath {
						return "", fmt.Errorf("wrong source")
					}
					return "demo", nil
				}
				// Positive attempt witness: Up enters this source gate only after the
				// profile contract gate. Legacy must get here; Alpha must stop beforehand.
				checked := false
				r.CheckSource = func(string) error { checked = true; return nil }
				NewSyncer(st, &Runner{Binary: gitBin}, r, 10*time.Second).sync(context.Background(), "demo")
				data, err := os.ReadFile(trace)
				if profile == "alpha" {
					if !os.IsNotExist(err) || checked {
						t.Fatalf("Alpha background source or deploy accessed: %q %v checked=%v", data, err, checked)
					}
					updated, e := st.Get("demo")
					if e != nil || updated.Git.LastSyncAt != nil {
						t.Fatalf("Alpha background status changed: %+v %v", updated, e)
					}
					return
				}
				if err != nil || string(data) != "old\npull\nnew\n" {
					t.Fatalf("changed HEAD not exercised: %q %v", data, err)
				}
				updated, err := st.Get("demo")
				if err != nil || updated.Git.LastCommit != "new" {
					t.Fatalf("pull status: %+v %v", updated, err)
				}
				if profile == "legacy" {
					if !checked {
						t.Fatal("Up source gate not reached")
					}
					select {
					case <-received:
					case <-time.After(5 * time.Second):
						t.Fatal("changed pull never deployed")
					}
				} else if checked {
					t.Fatal("Alpha bypassed pre-execution gate")
				}
				mu.Lock()
				defer mu.Unlock()
				if len(calls[0]) != 0 {
					t.Fatalf("wrong writes: %v", calls[0])
				}
				if profile == "alpha" {
					if len(calls[1]) != 0 {
						t.Fatalf("Alpha background mutation: %v", calls[1])
					}
				} else {
					if len(calls[1]) != 1 || !strings.Contains(calls[1][0], "-p demo --engine "+kind+" --engine-socket "+sockets[1]+" up") {
						t.Fatalf("unpinned background deployment: %v", calls[1])
					}
				}
			})
		}
	}
}
