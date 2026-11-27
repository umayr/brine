package brokerapp

import (
	"context"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/dill"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type TargetPolicy struct {
	Kind   string                  `json:"kind"`
	Local  *dill.LocalBrokerConfig `json:"local,omitempty"`
	HTTPS  *HTTPSTargetPolicy      `json:"https,omitempty"`
	Grants []string                `json:"grants"`
}
type HTTPSTargetPolicy struct {
	PairingFile        string                     `json:"pairingFile"`
	DeployTokenFile    string                     `json:"deployTokenFile"`
	Approved           agentclient.OperatorTarget `json:"approved"`
	BrokerTargetDigest string                     `json:"brokerTargetDigest,omitempty"`
}

func privateBytes(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnavailable
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, ErrUnavailable
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		clear(raw)
		return nil, ErrUnavailable
	}
	return raw, nil
}
func (s *Service) OpenTarget(ctx context.Context, id string, current func(context.Context) bool) (Target, func(), error) {
	if !s.Enabled() || current == nil || !current(ctx) {
		return nil, nil, ErrUnavailable
	}
	p, ok := s.policy.Targets[id]
	if !ok {
		return nil, nil, ErrUnavailable
	}
	if p.Kind == "local" && p.Local != nil && p.HTTPS == nil {
		client, err := dill.NewLocalBroker(*p.Local, p.Grants, current)
		if err != nil {
			return nil, nil, ErrUnavailable
		}
		return &LocalTarget{client}, func() {}, nil
	}
	if p.Kind != "https" || p.HTTPS == nil || p.Local != nil {
		return nil, nil, ErrUnavailable
	}
	raw, err := privateBytes(p.HTTPS.PairingFile, 16384)
	if err != nil {
		return nil, nil, err
	}
	pair, err := agentclient.ParsePairing(raw)
	clear(raw)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	token, err := privateBytes(p.HTTPS.DeployTokenFile, 44)
	if err != nil {
		return nil, nil, err
	}
	defer clear(token)
	grants := agentclient.BrokerGrants(0)
	seen := map[string]bool{}
	names := map[string]agentclient.BrokerGrants{"stage": agentclient.BrokerStage, "manifest": agentclient.BrokerManifest, "requirements": agentclient.BrokerRequirements, "prepare": agentclient.BrokerPrepare, "retire": agentclient.BrokerRetire, "review": agentclient.BrokerReview, "apply": agentclient.BrokerApply, "status": agentclient.BrokerStatus, "fence": agentclient.BrokerFence, "recovery-status": agentclient.BrokerRecoveryStatus}
	for _, name := range p.Grants {
		bit, ok := names[name]
		if !ok || seen[name] {
			return nil, nil, ErrUnavailable
		}
		seen[name] = true
		grants |= bit
	}
	var brokerTargets []string
	if p.HTTPS.BrokerTargetDigest != "" {
		brokerTargets = []string{p.HTTPS.BrokerTargetDigest}
	}
	client, epoch, err := pair.OpenBrokerDeploy(ctx, p.HTTPS.Approved, strings.TrimSuffix(string(token), "\n"), grants, current, brokerTargets...)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	return &HTTPSTarget{client, epoch}, func() { _ = client.Close() }, nil
}

type LocalTarget struct{ Client *dill.LocalBroker }

func (t *LocalTarget) Engine() string { return t.Client.Engine() }
func (t *LocalTarget) Info(ctx context.Context) (TargetInfo, error) {
	j, e := t.Client.Journal(ctx)
	return TargetInfo{j.JournalID, j.TargetDigest, j.EndpointDigest}, e
}
func (t *LocalTarget) Requirements(ctx context.Context, j, stage, manifest, stack string) (brokerwire.Requirements, error) {
	return t.Client.Requirements(ctx, j, stage, manifest, stack)
}
func (t *LocalTarget) Prepare(ctx context.Context, j, stage string, e brokerwire.Envelope) (brokerwire.Review, error) {
	return t.Client.Prepare(ctx, j, stage, e)
}
func (t *LocalTarget) Review(ctx context.Context, j, stage string, r brokerwire.Reference) (*dill.BrokerReview, string, error) {
	return t.Client.Review(ctx, j, stage, r)
}
func (t *LocalTarget) Apply(ctx context.Context, j, stage, id, selection, mode string, r brokerwire.Reference) (Outcome, error) {
	o, e := t.Client.Apply(ctx, j, stage, id, selection, mode, r)
	s := o.Status
	return Outcome{s.RequestID, s.Stack, s.State, s.Effects, s.OperationID}, e
}
func (t *LocalTarget) Status(ctx context.Context, j, id string, r brokerwire.Reference) (Outcome, error) {
	o, e := t.Client.Status(ctx, j, id, r)
	s := o.Status
	return Outcome{s.RequestID, s.Stack, s.State, s.Effects, s.OperationID}, e
}

type HTTPSTarget struct {
	Client *agentclient.BrokerClient
	Epoch  string
}

func (t *HTTPSTarget) Engine() string { return t.Client.Engine() }
func (t *HTTPSTarget) Info(ctx context.Context) (TargetInfo, error) {
	j, e := t.Client.Journal(ctx, t.Epoch)
	return TargetInfo{j.JournalID, j.TargetDigest, j.EndpointDigest}, e
}
func (t *HTTPSTarget) Requirements(ctx context.Context, j, stage, manifest, stack string) (brokerwire.Requirements, error) {
	return t.Client.Requirements(ctx, t.Epoch, j, stage, manifest, stack)
}
func (t *HTTPSTarget) Prepare(ctx context.Context, j, stage string, e brokerwire.Envelope) (brokerwire.Review, error) {
	return t.Client.Prepare(ctx, t.Epoch, j, stage, e)
}
func (t *HTTPSTarget) Review(ctx context.Context, j, stage string, r brokerwire.Reference) (*dill.BrokerReview, string, error) {
	return t.Client.Review(ctx, t.Epoch, j, stage, r)
}
func (t *HTTPSTarget) Apply(ctx context.Context, j, stage, id, selection, mode string, r brokerwire.Reference) (Outcome, error) {
	o, e := t.Client.Apply(ctx, t.Epoch, j, stage, id, selection, mode, r)
	s := o.Status
	return Outcome{s.RequestID, s.Stack, s.State, s.Effects, s.OperationID}, e
}
func (t *HTTPSTarget) Status(ctx context.Context, j, id string, r brokerwire.Reference) (Outcome, error) {
	o, e := t.Client.Status(ctx, t.Epoch, j, id, r)
	s := o.Status
	return Outcome{s.RequestID, s.Stack, s.State, s.Effects, s.OperationID}, e
}
