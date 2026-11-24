package dill

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

var ErrProcessUnknown = errors.New("local process cleanup could not be confirmed; outcome unknown")

// Own the pipes so a retained process cannot keep writing into returned buffers.
// Callers supply finite stdin and nonblocking/bounded output writers.
type processIO struct {
	child, parent []*os.File
	wg            sync.WaitGroup
	mu            sync.Mutex
	err           error
}

func ownProcessIO(cmd *exec.Cmd) (*processIO, error) {
	p := &processIO{}
	output := func(dst io.Writer) (*os.File, error) {
		rd, wr, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		p.child = append(p.child, wr)
		p.parent = append(p.parent, rd)
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer rd.Close()
			buf := make([]byte, 32768)
			for {
				n, err := rd.Read(buf)
				if n > 0 {
					p.mu.Lock()
					written, we := dst.Write(buf[:n])
					if we == nil && written != n {
						we = io.ErrShortWrite
					}
					p.err = errors.Join(p.err, we)
					p.mu.Unlock()
					if we != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		return wr, nil
	}
	var err error
	if cmd.Stdout != nil {
		cmd.Stdout, err = output(cmd.Stdout)
		if err != nil {
			p.finish(false)
			return nil, err
		}
	}
	if cmd.Stderr != nil {
		cmd.Stderr, err = output(cmd.Stderr)
		if err != nil {
			p.finish(false)
			return nil, err
		}
	}
	if cmd.Stdin != nil {
		src := cmd.Stdin
		rd, wr, e := os.Pipe()
		if e != nil {
			p.finish(false)
			return nil, e
		}
		p.child = append(p.child, rd)
		p.parent = append(p.parent, wr)
		cmd.Stdin = rd
		p.wg.Add(1)
		go func() { defer p.wg.Done(); defer wr.Close(); _, _ = io.Copy(wr, src) }()
	}
	return p, nil
}
func (p *processIO) started() {
	for _, f := range p.child {
		_ = f.Close()
	}
}
func (p *processIO) finish(drain bool) error {
	p.started()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	var drainErr error
	if drain {
		select {
		case <-done:
		case <-time.After(time.Second):
			drainErr = exec.ErrWaitDelay
		}
	}
	for _, f := range p.parent {
		_ = f.Close()
	}
	<-done
	return errors.Join(p.err, drainErr)
}
