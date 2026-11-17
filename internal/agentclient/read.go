package agentclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// RemoteError contains only an allowlisted code, never agent-provided text.
type RemoteError struct {
	Code      string
	Retryable bool
}

func (e *RemoteError) Error() string { return "agent: " + e.Code }

var ErrIncompleteStream = errors.New("agent stream incomplete")
var statusCodes = map[string]int{"INVALID": 400, "UNAUTHORIZED": 403, "REPLAY": 403, "NOT_FOUND": 404, "BINDING_CHANGED": 409, "UNSUPPORTED_VERSION": 426, "LIMIT_EXCEEDED": 413, "UNSUPPORTED": 501, "ENGINE_UNAVAILABLE": 503, "DEADLINE": 504, "INTERNAL": 500}

func decodeStrict(raw []byte, v any) error {
	if !utf8.Valid(raw) || checkJSON(raw) != nil {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrProtocol
	}
	return nil
}
func (c *Client) request(ctx context.Context, activeCtx context.Context, path, epoch string, body any) (*http.Response, string, error) {
	if _, e := binaryText(epoch, 32); e != nil {
		return nil, "", ErrProtocol
	}
	c.mu.Lock()
	valid := !c.dead && c.forward.Err() == nil && c.epoch == epoch
	sessionCtx := c.epochCtx
	c.mu.Unlock()
	if !valid {
		return nil, "", ErrProtocol
	}
	raw, e := json.Marshal(body)
	if e != nil || len(raw) > 32768 {
		return nil, "", ErrProtocol
	}
	id := make([]byte, 16)
	if _, e = rand.Read(id); e != nil {
		return nil, "", ErrProtocol
	}
	rid := hex.EncodeToString(id)
	requestCtx, cancel := context.WithCancel(ctx)
	// Forward lifetime and explicit Close remain attached even if this request
	// retires the epoch after receiving an error status.
	stopForward := context.AfterFunc(c.forward, cancel)
	stop := func() bool { return false }
	if sessionCtx != nil {
		stop = context.AfterFunc(sessionCtx, cancel)
	}
	stopActive := func() bool { return false }
	if activeCtx != nil {
		stopActive = context.AfterFunc(activeCtx, cancel)
	}
	req, e := http.NewRequestWithContext(requestCtx, "POST", "http://unix"+path, bytes.NewReader(raw))
	if e != nil {
		stop()
		stopActive()
		stopForward()
		cancel()
		return nil, "", ErrProtocol
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("X-Brine-Binding", fmt.Sprint(c.binding.Generation))
	req.Header.Set("X-Request-Id", rid)
	req.Header.Set("X-Agent-Epoch", epoch)
	res, e := c.http.Do(req)
	if e != nil {
		stop()
		stopActive()
		stopForward()
		cancel()
		c.fail()
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", ErrProtocol
	}
	// Retire the proof before inspecting any error headers or body. Detach this
	// response from the epoch cancellation so a bounded error body may be parsed.
	if res.StatusCode != http.StatusOK {
		stop()
		stopActive()
		c.fail()
	}
	res.Body = &cancelBody{ReadCloser: res.Body, cancel: func() {
		stop()
		stopActive()
		stopForward()
		cancel()
		c.mu.Lock()
		dead := c.dead
		c.mu.Unlock()
		if dead {
			c.http.CloseIdleConnections()
		}
	}}
	return res, rid, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel func()
}

func (b *cancelBody) Close() error { e := b.ReadCloser.Close(); b.cancel(); return e }

type envelope struct {
	Version   string          `json:"version"`
	RequestID string          `json:"requestId"`
	Data      json.RawMessage `json:"data"`
	Error     *struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

func parseEnvelope(raw []byte, rid string, status int) (json.RawMessage, error) {
	var e envelope
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil || len(members) != 3 || members["version"] == nil || members["requestId"] == nil || (members["data"] == nil) == (members["error"] == nil) {
		return nil, ErrProtocol
	}
	if decodeStrict(raw, &e) != nil || e.Version != version || e.RequestID != rid {
		return nil, ErrProtocol
	}
	if status == 200 {
		if e.Error != nil || len(e.Data) == 0 || string(e.Data) == "null" {
			return nil, ErrProtocol
		}
		return e.Data, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || e.Error == nil || requireFields(fields["error"], reflect.TypeOf(e.Error)) != nil || len(e.Data) != 0 || statusCodes[e.Error.Code] != status || e.Error.Retryable != (e.Error.Code == "ENGINE_UNAVAILABLE" || e.Error.Code == "DEADLINE") {
		return nil, ErrProtocol
	}
	return nil, &RemoteError{e.Error.Code, e.Error.Retryable}
}
func (c *Client) call(ctx context.Context, path, epoch string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, id, e := c.request(ctx, nil, path, epoch, body)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != "application/json" {
		return ErrProtocol
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 512*1024+1))
	if e != nil || len(raw) > 512*1024 {
		c.fail()
		return ErrProtocol
	}
	data, e := parseEnvelope(raw, id, res.StatusCode)
	if e != nil {
		c.invalidateOnBindingChange(epoch, e)
		return e
	}
	if e := requireFields(data, reflect.TypeOf(out)); e != nil {
		return e
	}
	return decodeStrict(data, out)
}

func (c *Client) invalidateOnBindingChange(epoch string, err error) {
	var re *RemoteError
	if !errors.As(err, &re) || re.Code != "BINDING_CHANGED" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch == epoch {
		c.epoch = ""
		if c.epochCancel != nil {
			c.epochCancel()
		}
	}
}

// requireFields distinguishes omitted members from explicitly null optional values.
func requireFields(raw []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return nil
		}
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil || m == nil {
			return ErrProtocol
		}
		if len(m) != typ.NumField() {
			return ErrProtocol
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			v, ok := m[name]
			if !ok {
				return ErrProtocol
			}
			if err := requireFields(v, f.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return ErrProtocol
		}
		for _, v := range values {
			if err := requireFields(v, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		if string(raw) == "null" {
			return ErrProtocol
		}
	default:
		if string(raw) == "null" {
			return ErrProtocol
		}
	}
	return nil
}

type InventoryItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Stack   string `json:"stack"`
	Service string `json:"service"`
	State   string `json:"state"`
	Image   string `json:"image"`
}
type InventoryPage struct {
	Items      []InventoryItem `json:"items"`
	NextCursor *string         `json:"nextCursor"`
	ObservedAt string          `json:"observedAt"`
}

func (c *Client) Inventory(ctx context.Context, epoch string, cursor *string, limit int) (InventoryPage, error) {
	var p InventoryPage
	if limit < 1 || limit > 100 {
		return p, ErrProtocol
	}
	e := c.call(ctx, "/v1/inventory", epoch, struct {
		Cursor *string `json:"cursor"`
		Limit  int     `json:"limit"`
	}{cursor, limit}, &p)
	if e != nil {
		return p, e
	}
	if p.Items == nil || len(p.Items) > limit || !timestamp(p.ObservedAt) {
		return InventoryPage{}, ErrProtocol
	}
	last := ""
	for _, x := range p.Items {
		if !fullID(x.ID) || x.ID <= last || x.Stack == "" || x.Service == "" || !stringsOK(x.ID, x.Name, x.Stack, x.Service, x.State, x.Image) {
			return InventoryPage{}, ErrProtocol
		}
		last = x.ID
	}
	return p, nil
}
func timestamp(s string) bool { _, e := time.Parse(time.RFC3339, s); return e == nil }
func stringsOK(ss ...string) bool {
	for _, s := range ss {
		if !utf8.ValidString(s) || len(s) > 8192 {
			return false
		}
	}
	return true
}
func listOK(a []string) bool {
	if a == nil || len(a) > 256 {
		return false
	}
	return stringsOK(a...)
}

type Port struct {
	Protocol      string `json:"protocol"`
	HostIP        string `json:"hostIp"`
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
}
type Mount struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
}
type Inspection struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	ImageID       string            `json:"imageId"`
	State         string            `json:"state"`
	CreatedAt     *string           `json:"createdAt"`
	Command       []string          `json:"command"`
	User          *string           `json:"user"`
	EnvKeys       []string          `json:"envKeys"`
	Labels        map[string]string `json:"labels"`
	Ports         []Port            `json:"ports"`
	Mounts        []Mount           `json:"mounts"`
	Networks      []string          `json:"networks"`
	RestartPolicy *string           `json:"restartPolicy"`
	Health        struct {
		Status *string  `json:"status"`
		Test   []string `json:"test"`
	} `json:"health"`
	Resources struct {
		MemoryBytes *int64 `json:"memoryBytes"`
		CPUNano     *int64 `json:"cpuNano"`
		PidsLimit   *int64 `json:"pidsLimit"`
	} `json:"resources"`
	Security struct {
		Privileged  *bool    `json:"privileged"`
		CapAdd      []string `json:"capAdd"`
		NetworkMode *string  `json:"networkMode"`
		PIDMode     *string  `json:"pidMode"`
		IPCMode     *string  `json:"ipcMode"`
	} `json:"security"`
}
type InspectResult struct {
	ContainerID string     `json:"containerId"`
	Stack       string     `json:"stack"`
	Service     string     `json:"service"`
	ObservedAt  string     `json:"observedAt"`
	Inspect     Inspection `json:"inspect"`
}

// Inspect returns a normalized profile, which can still contain sensitive commands and paths.
func (c *Client) Inspect(ctx context.Context, epoch, id string) (InspectResult, error) {
	var r InspectResult
	if !fullID(id) {
		return r, ErrProtocol
	}
	if e := c.call(ctx, "/v1/inspect", epoch, struct {
		ID string `json:"containerId"`
	}{id}, &r); e != nil {
		return r, e
	}
	x := r.Inspect
	if r.ContainerID != id || x.ID != id || r.Stack == "" || r.Service == "" || !timestamp(r.ObservedAt) || !stringsOK(r.Stack, r.Service, x.ID, x.Name, x.Image, x.ImageID, x.State) || !listOK(x.Command) || !listOK(x.EnvKeys) || !listOK(x.Networks) || !listOK(x.Health.Test) || !listOK(x.Security.CapAdd) || len(x.Labels) != 3 || x.Labels["dill.managed"] != "true" || x.Labels["dill.stack"] != r.Stack || x.Labels["dill.service"] != r.Service || x.Ports == nil || x.Mounts == nil || len(x.Ports) > 256 || len(x.Mounts) > 256 {
		return InspectResult{}, ErrProtocol
	}
	if !sort.StringsAreSorted(x.EnvKeys) {
		return InspectResult{}, ErrProtocol
	}
	for i, s := range x.EnvKeys {
		if s == "" || strings.Contains(s, "=") || (i > 0 && x.EnvKeys[i-1] == s) {
			return InspectResult{}, ErrProtocol
		}
	}
	for _, p := range x.Ports {
		if p.HostPort < 0 || p.HostPort > 65535 || p.ContainerPort < 0 || p.ContainerPort > 65535 || !stringsOK(p.Protocol, p.HostIP) {
			return InspectResult{}, ErrProtocol
		}
	}
	for _, m := range x.Mounts {
		if !stringsOK(m.Type, m.Source, m.Destination) {
			return InspectResult{}, ErrProtocol
		}
	}
	for _, v := range []*int64{x.Resources.MemoryBytes, x.Resources.CPUNano, x.Resources.PidsLimit} {
		if v != nil && *v < 0 {
			return InspectResult{}, ErrProtocol
		}
	}
	for _, v := range []*string{x.CreatedAt, x.User, x.RestartPolicy, x.Health.Status, x.Security.NetworkMode, x.Security.PIDMode, x.Security.IPCMode} {
		if v != nil && !stringsOK(*v) {
			return InspectResult{}, ErrProtocol
		}
	}
	return r, nil
}

// RevealResult contains secrets. Callers must not persist or log it.
type RevealResult struct {
	ContainerID string `json:"containerId"`
	Env         []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"env"`
}

// Reveal consumes a grant locally before issuing the request, including on transport failure.
func (c *Client) Reveal(ctx context.Context, epoch, id string, g Grant) (RevealResult, error) {
	var r RevealResult
	h := shaAgent(c.binding.AgentPublicKey)
	now := time.Now().Unix()
	if now < 0 || VerifyGrant(c.revealKey, g, h, c.binding.EngineID, c.binding.Generation, epoch, id, uint64(now)) != nil {
		return r, ErrProtocol
	}
	if !c.consume(g) {
		return r, ErrProtocol
	}
	e := c.call(ctx, "/v1/reveal", epoch, struct {
		ID    string `json:"containerId"`
		Grant Grant  `json:"grant"`
	}{id, g}, &r)
	if e != nil {
		return RevealResult{}, e
	}
	if r.ContainerID != id || r.Env == nil || len(r.Env) > 256 {
		return RevealResult{}, ErrProtocol
	}
	seen := map[string]bool{}
	for _, v := range r.Env {
		if v.Key == "" || strings.Contains(v.Key, "=") || seen[v.Key] || !stringsOK(v.Key, v.Value) {
			return RevealResult{}, ErrProtocol
		}
		seen[v.Key] = true
	}
	return r, nil
}
