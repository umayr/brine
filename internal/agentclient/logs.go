package agentclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"time"
)

func shaAgent(key []byte) string { h := sha256.Sum256(key); return hex.EncodeToString(h[:]) }
func (c *Client) consume(g Grant) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().Unix()
	if now < 0 {
		return false
	}
	for k, v := range c.used {
		if v+5 < uint64(now) {
			delete(c.used, k)
		}
	}
	k := g.Epoch + ":" + g.Nonce
	if _, ok := c.used[k]; ok || len(c.used) >= 10000 {
		return false
	}
	c.used[k] = g.ExpiresAt
	return true
}

type LogOptions struct {
	Tail       int
	Follow     bool
	Timestamps bool
}
type LogEvent struct {
	Type     string
	Sequence uint64
	Stream   string
	Bytes    []byte
	Reason   string
	Total    int
	Code     string
}

// Logs delivers untrusted, potentially secret bytes without retaining them.
// The sink runs synchronously and must observe its context and return promptly.
// No sink runs after Logs returns; an uncooperative sink blocks teardown.
// A cooperative sink gets a five-second deadline and must not persist bytes.
func (c *Client) Logs(ctx context.Context, epoch, id string, opts LogOptions, emit func(context.Context, LogEvent) error) error {
	if !fullID(id) || opts.Tail < 1 || opts.Tail > 1000 || emit == nil {
		return ErrProtocol
	}
	select {
	case c.streams <- struct{}{}:
		defer func() { <-c.streams }()
	default:
		return ErrProtocol
	}
	duration := 15 * time.Second
	if opts.Follow {
		duration = 5 * time.Minute
	}
	callerCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopForward := context.AfterFunc(c.forward, cancel)
	defer stopForward()
	c.mu.Lock()
	epochCtx := c.epochCtx
	c.mu.Unlock()
	if epochCtx != nil {
		stopEpoch := context.AfterFunc(epochCtx, cancel)
		defer stopEpoch()
	}
	clock := newStreamClock(duration, opts.Follow, cancel)
	defer clock.stop()
	body := struct {
		ID         string `json:"containerId"`
		Tail       int    `json:"tail"`
		Follow     bool   `json:"follow"`
		Timestamps bool   `json:"timestamps"`
	}{id, opts.Tail, opts.Follow, opts.Timestamps}
	res, rid, e := c.request(callerCtx, ctx, "/v1/logs", epoch, body)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		// This response is detached from the retired epoch, but remains bounded
		// by the caller context and an independent deadline.
		errorDeadline := time.AfterFunc(15*time.Second, func() { res.Body.Close() })
		defer errorDeadline.Stop()
		if res.Header.Get("Content-Type") != "application/json" {
			return ErrProtocol
		}
		raw, e := io.ReadAll(io.LimitReader(res.Body, 512*1024+1))
		if e != nil || len(raw) > 512*1024 {
			if callerCtx.Err() != nil {
				return callerCtx.Err()
			}
			return ErrProtocol
		}
		c.fail()
		_, e = parseEnvelope(raw, rid, res.StatusCode)
		c.invalidateOnBindingChange(epoch, e)
		return e
	}
	if res.Header.Get("Content-Type") != "application/x-ndjson" {
		return ErrProtocol
	}
	s := bufio.NewScanner(res.Body)
	s.Buffer(make([]byte, 4096), 32*1024)
	s.Split(func(data []byte, eof bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i], nil
		}
		if eof && len(data) > 0 {
			return 0, nil, ErrProtocol
		}
		return 0, nil, nil
	})
	var seq uint64
	total := 0
	for s.Scan() {
		if !clock.activity() {
			return ErrProtocol
		}
		data, e := parseEnvelope(s.Bytes(), rid, 200)
		if e != nil {
			return e
		}
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &kind) != nil {
			return ErrProtocol
		}
		var ev LogEvent
		ev.Type = kind.Type
		switch kind.Type {
		case "chunk":
			var x struct {
				Type     string `json:"type"`
				Sequence uint64 `json:"sequence"`
				Stream   string `json:"stream"`
				Bytes    string `json:"bytesBase64"`
			}
			if (requireFields(data, reflect.TypeOf(x)) != nil || decodeStrict(data, &x) != nil) || x.Sequence != seq+1 || (x.Stream != "stdout" && x.Stream != "stderr" && x.Stream != "unknown") {
				return ErrProtocol
			}
			b, e := base64.StdEncoding.DecodeString(x.Bytes)
			if e != nil || base64.StdEncoding.EncodeToString(b) != x.Bytes || len(b) > 16384 || total+len(b) > 1048576 {
				return ErrProtocol
			}
			seq = x.Sequence
			total += len(b)
			ev.Sequence = seq
			ev.Stream = x.Stream
			ev.Bytes = b
		case "heartbeat":
			var x struct {
				Type     string `json:"type"`
				Sequence uint64 `json:"sequence"`
			}
			if (requireFields(data, reflect.TypeOf(x)) != nil || decodeStrict(data, &x) != nil) || x.Sequence != seq+1 {
				return ErrProtocol
			}
			seq = x.Sequence
			ev.Sequence = seq
		case "end":
			var x struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
				Bytes  int    `json:"bytes"`
			}
			if (requireFields(data, reflect.TypeOf(x)) != nil || decodeStrict(data, &x) != nil) || (x.Reason != "complete" && x.Reason != "duration" && x.Reason != "byte_limit" && x.Reason != "cancelled") || x.Bytes != total {
				return ErrProtocol
			}
			ev.Reason = x.Reason
			ev.Total = total
			if !clock.acceptEnd() {
				return ErrProtocol
			}
			grace := time.AfterFunc(5*time.Second, cancel)
			defer grace.Stop()
			if s.Scan() || s.Err() != nil || ctx.Err() != nil {
				return ErrProtocol
			}
			grace.Stop()
			return c.emitLog(ctx, emit, ev)
		case "error":
			c.fail()
			var x struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if (requireFields(data, reflect.TypeOf(x)) != nil || decodeStrict(data, &x) != nil) || statusCodes[x.Code] == 0 {
				return ErrProtocol
			}
			err := &RemoteError{Code: x.Code, Retryable: x.Code == "ENGINE_UNAVAILABLE" || x.Code == "DEADLINE"}
			c.invalidateOnBindingChange(epoch, err)
			return err
		default:
			return ErrProtocol
		}
		if e = c.emitLog(ctx, emit, ev); e != nil {
			return e
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.Err() != nil {
		return ErrProtocol
	}
	return ErrIncompleteStream
}

func (c *Client) emitLog(ctx context.Context, emit func(context.Context, LogEvent) error, ev LogEvent) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	sinkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := emit(sinkCtx, ev)
	if sinkCtx.Err() != nil {
		return sinkCtx.Err()
	}
	return err
}
