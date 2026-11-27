package webhooks

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

func (s *Service) Run(ctx context.Context) error {
	s.lifecycle.Lock()
	if s.closed || !s.running.CompareAndSwap(false, true) {
		s.lifecycle.Unlock()
		return ErrConflict
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	done := s.done
	s.lifecycle.Unlock()
	defer func() { s.lifecycle.Lock(); close(done); s.running.Store(false); s.lifecycle.Unlock() }()
	if ctx.Err() != nil {
		return nil
	}
	if e := s.recoverClaims(ctx); e != nil {
		if ctx.Err() != nil {
			return nil
		}
		return e
	}
	stopSampling := s.startSampling(ctx)
	defer stopSampling()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if s.now().Sub(lastPrune) >= 10*time.Minute {
			if e := s.prune(ctx); e != nil {
				if ctx.Err() != nil {
					return nil
				}
				return e
			}
			lastPrune = s.now()
		}
		if e := s.ReconcileMetadata(ctx); e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		if e := s.reconcileOperations(ctx); e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		sent, e := s.sendOne(ctx)
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		if sent {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type claimedDelivery struct {
	Delivery
	url             string
	secret, payload []byte
}

func (s *Service) claim(ctx context.Context) (claimedDelivery, error) {
	var c claimedDelivery
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return c, errStorage
	}
	defer tx.Rollback()
	// Paused endpoints retain their backlog without blocking other receivers.
	query := "SELECT " + deliveryColumns + ",d.url,d.secret FROM deliveries d JOIN events e ON e.id=d.event_id JOIN endpoints ep ON ep.id=d.endpoint_id WHERE d.status='pending' AND d.attempts<100 AND d.next_at<=? AND json_extract(CAST(ep.config AS TEXT),'$.enabled')=1 AND d.url=json_extract(CAST(ep.config AS TEXT),'$.url') AND d.secret=ep.secret ORDER BY d.next_at,d.id LIMIT 1"
	var status, category string
	var next, created, updated int64
	e = tx.QueryRowContext(ctx, query, encodeTime(s.now())).Scan(&c.ID, &c.EndpointID, &c.payload, &status, &c.Attempts, &c.HTTPStatus, &category, &next, &created, &updated, &c.url, &c.secret)
	if errors.Is(e, sql.ErrNoRows) {
		return c, sql.ErrNoRows
	}
	if e != nil {
		return c, errStorage
	}
	c.CreatedAt = time.UnixMilli(created)
	if category != "operator_redelivery" && !s.now().Before(c.CreatedAt.Add(24*time.Hour)) {
		if _, e = tx.ExecContext(ctx, "UPDATE deliveries SET status='exhausted',error_category='retry_window_elapsed',updated=? WHERE id=?", encodeTime(s.now()), c.ID); e != nil {
			return c, errStorage
		}
		if tx.Commit() != nil {
			return c, errStorage
		}
		return c, sql.ErrNoRows
	}
	c.Attempts++
	if _, e = tx.ExecContext(ctx, "UPDATE deliveries SET status='sending',attempts=?,updated=? WHERE id=?", c.Attempts, encodeTime(s.now()), c.ID); e != nil {
		return c, errStorage
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO attempts(delivery_id,number,at,duration,http_status,error_category) VALUES(?,?,?,0,0,'delivery_unconfirmed')", c.ID, c.Attempts, encodeTime(s.now())); e != nil {
		return c, errStorage
	}
	if tx.Commit() != nil {
		return c, errStorage
	}
	return c, nil
}

func (s *Service) sendOne(ctx context.Context) (bool, error) {
	c, e := s.claim(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	started := s.now()
	status := 0
	category := ""
	retry := false
	retryAfter := time.Duration(0)
	key, e := s.unseal(c.secret)
	if e != nil {
		category = "signing_unavailable"
	} else if ValidateDestination(c.url) != nil {
		category = "destination_denied"
	} else {
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(c.payload))
		if e != nil {
			category = "destination_denied"
		} else {
			timestamp := strconv.FormatInt(s.now().Unix(), 10)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "Brine-Webhooks/1")
			req.Header.Set("Brine-Event-ID", extractEventID(c.payload))
			req.Header.Set("Brine-Delivery-ID", c.ID)
			req.Header.Set("Brine-Timestamp", timestamp)
			req.Header.Set("Brine-Signature", Sign(key, timestamp, c.payload))
			resp, err := s.client.Do(req)
			if err != nil {
				category = "network_unavailable"
				retry = true
			} else {
				status = resp.StatusCode
				// Bound bytes read; do not store or log any receiver body.
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				retry = status == 408 || status == 429 || status >= 500
				if status < 200 || status >= 300 {
					category = "receiver_rejected"
				}
				retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), s.now())
			}
		}
	}
	clear(key)
	attempt := c.Attempts
	at := s.now()
	state := "exhausted"
	next := at
	if status >= 200 && status < 300 {
		state = "delivered"
	} else if retry && attempt < 8 && at.Sub(c.CreatedAt) < 24*time.Hour {
		state = "pending"
		delay := retryDelay(attempt)
		if retryAfter > delay {
			delay = retryAfter
		}
		next = at.Add(delay)
		if next.Sub(c.CreatedAt) > 24*time.Hour {
			state = "exhausted"
		}
	}
	// Commit even when the request context was cancelled: preserve the observed
	// transport result, but never use it as an execution result. Bound the write.
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, e := s.db.BeginTx(saveCtx, nil)
	if e != nil {
		return true, errStorage
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(saveCtx, "UPDATE attempts SET at=?,duration=?,http_status=?,error_category=? WHERE delivery_id=? AND number=?", encodeTime(at), at.Sub(started).Milliseconds(), status, category, c.ID, attempt); e != nil {
		return true, errStorage
	}
	var current int
	if tx.QueryRowContext(saveCtx, "SELECT COUNT(*) FROM endpoints WHERE id=? AND json_extract(CAST(config AS TEXT),'$.url')=? AND secret=?", c.EndpointID, c.url, c.secret).Scan(&current) != nil {
		return true, errStorage
	}
	if current == 0 {
		state = "cancelled"
		category = "endpoint_retired"
	}
	if _, e = tx.ExecContext(saveCtx, "UPDATE deliveries SET status=?,attempts=?,http_status=?,error_category=?,next_at=?,updated=? WHERE id=?", state, attempt, status, category, encodeTime(next), encodeTime(at), c.ID); e != nil {
		return true, errStorage
	}
	if tx.Commit() != nil {
		return true, errStorage
	}
	return true, nil
}

func extractEventID(body []byte) string {
	var e Event
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.ID
}

func retryDelay(attempt int) time.Duration {
	if attempt > 7 {
		attempt = 7
	}
	base := time.Duration(1<<uint(attempt-1)) * 30 * time.Second
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		return base
	}
	return base + time.Duration(binary.BigEndian.Uint64(b[:])%uint64(base/4+1))
}
func parseRetryAfter(raw string, now time.Time) time.Duration {
	const capDelay = time.Hour
	if seconds, e := strconv.ParseInt(raw, 10, 64); e == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 3600 {
			return capDelay
		}
		return time.Duration(seconds) * time.Second
	}
	if at, e := http.ParseTime(raw); e == nil {
		d := at.Sub(now)
		if d < 0 {
			return 0
		}
		if d > capDelay {
			return capDelay
		}
		return d
	}
	return 0
}
