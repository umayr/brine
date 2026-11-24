package api

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// outputObserver isolates operation output from its HTTP observer. Overflow
// ends delivery explicitly, never operation execution. Only the consumer writes
// HTTP; Finish joins it before the handler relinquishes the ResponseWriter.
type outputObserver struct {
	http.ResponseWriter
	mu       sync.Mutex
	queue    chan []byte
	done     chan struct{}
	failed   bool
	closed   bool
	finishBy time.Time
}

const observerWriteTimeout = 2 * time.Second

func newOutputObserver(w http.ResponseWriter) (*outputObserver, error) {
	if !supportsOutputFlush(w) {
		return nil, errors.New("bounded output flushing unavailable")
	}
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		return nil, errors.New("bounded output delivery unavailable")
	}
	o := &outputObserver{ResponseWriter: w, queue: make(chan []byte, 64), done: make(chan struct{})}
	go o.deliver()
	return o, nil
}
func (o *outputObserver) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	if o.failed || o.closed {
		return n, nil
	}
	for len(p) > 0 {
		size := min(len(p), 4096)
		chunk := append([]byte(nil), p[:size]...)
		select {
		case o.queue <- chunk:
			p = p[size:]
		default:
			o.failed = true
			return n, nil
		}
	}
	return n, nil
}
func (o *outputObserver) Finish() {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		o.finishBy = time.Now().Add(observerWriteTimeout)
		close(o.queue)
	}
	o.mu.Unlock()
	<-o.done
}
func (o *outputObserver) deliver() {
	defer close(o.done)
	rc := http.NewResponseController(o.ResponseWriter)
	defer rc.SetWriteDeadline(time.Time{})
	for p := range o.queue {
		o.mu.Lock()
		failed := o.failed
		end := o.finishBy
		o.mu.Unlock()
		if failed {
			p = []byte("\n[brine:error] Output delivery incomplete; inspect stack state for the operation outcome.\n")
		}
		deadline := time.Now().Add(observerWriteTimeout)
		if !end.IsZero() && end.Before(deadline) {
			deadline = end
		}
		err := rc.SetWriteDeadline(deadline)
		if err == nil {
			var n int
			n, err = o.ResponseWriter.Write(p)
			if err == nil && n != len(p) {
				err = io.ErrShortWrite
			}
		}
		if err == nil {
			err = rc.Flush()
		}
		if err == nil {
			err = rc.SetWriteDeadline(time.Time{})
		}
		if err != nil || failed {
			o.mu.Lock()
			o.failed = true
			o.mu.Unlock()
			return
		}
	}
}
