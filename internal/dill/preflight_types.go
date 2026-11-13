package dill

import "errors"

const (
	PreflightVersion             = "dill.preflight/1"
	PreflightCapabilitiesVersion = "dill.preflight-capabilities/1"
	PreflightMaxResources        = 256
	PreflightMaxNameBytes        = 255
	PreflightMaxOutputBytes      = 262144
)

// These errors deliberately contain no provider output, field names or inputs.
var (
	ErrPreflightContract  = errors.New("Dill preflight evidence unavailable or invalid")
	ErrPreflightBinding   = errors.New("Dill preflight evidence does not match trusted review context")
	ErrPreflightArguments = errors.New("Dill preflight structural arguments unavailable or invalid")
)

// PreflightCapabilities is static discovery, never native placement or permission.
type PreflightCapabilities struct {
	Version       string           `json:"version"`
	Supported     bool             `json:"supported"`
	Contract      string           `json:"contract,omitempty"`
	ApplyContract string           `json:"applyContract,omitempty"`
	Operations    []string         `json:"operations,omitempty"`
	Engines       []string         `json:"engines,omitempty"`
	Layouts       []string         `json:"layouts,omitempty"`
	Limits        *PreflightLimits `json:"limits,omitempty"`
}
type PreflightLimits struct {
	MaxResources   int `json:"maxResources"`
	MaxNameBytes   int `json:"maxNameBytes"`
	MaxOutputBytes int `json:"maxOutputBytes"`
}

// PreflightTarget is a claim of actual retained qualification, not a selector.
// Consumer syntax validation cannot establish the provenance of that claim.
type PreflightTarget struct {
	Engine                  string `json:"engine"`
	EndpointDigest          string `json:"endpointDigest"`
	QualifiedIdentityDigest string `json:"qualifiedIdentityDigest"`
}
type PreflightScope struct {
	Scope        string `json:"scope"`
	Count        int    `json:"count"`
	PresentCount int    `json:"presentCount"`
	Complete     bool   `json:"complete"`
}
type PreflightCoverage struct {
	Containers PreflightScope `json:"containers"`
	Network    PreflightScope `json:"network"`
	Volumes    PreflightScope `json:"volumes"`
}
type PreflightResource struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Identity     string `json:"identity"`
	IdentityKind string `json:"identityKind"`
	Presence     string `json:"presence"`
	Action       string `json:"action"`
	Retention    string `json:"retention"`
}

// PreflightResult is complete review evidence, never predicted apply success.
// Digests are opaque provider commitments, not reproducible consumer assertions.
type PreflightResult struct {
	Version             string                `json:"version"`
	OperationID         string                `json:"operationId"`
	Operation           string                `json:"operation"`
	Target              *PreflightTarget      `json:"target"`
	Stack               string                `json:"stack"`
	Outcome             string                `json:"outcome"`
	EngineEffects       string                `json:"engineEffects"`
	CoordinationEffects string                `json:"coordinationEffects"`
	InputDigest         *string               `json:"inputDigest"`
	SelectionDigest     *string               `json:"selectionDigest"`
	Coverage            PreflightCoverage     `json:"coverage"`
	Resources           []PreflightResource   `json:"resources"`
	ResourcesTruncated  bool                  `json:"resourcesTruncated"`
	Errors              []OperationDiagnostic `json:"errors"`
}

// PreflightInvocation preserves independent process and result axes. An invalid
// or missing process exit or invalid delivery always yields unknown + nil result.
type PreflightInvocation struct {
	Result      *PreflightResult `json:"result"`
	ProcessExit *int             `json:"processExit"`
	Status      string           `json:"status"`
}

type PreflightBinding struct {
	Stack, Operation, Engine                string
	EndpointDigest, QualifiedIdentityDigest string
}

// CheckPreflightBinding checks evidence against independently trusted expectations.
// It grants no execution permission and does not authenticate caller-supplied data.
func CheckPreflightBinding(v *PreflightInvocation, want PreflightBinding) error {
	if v == nil || v.Result == nil || v.ProcessExit == nil || *v.ProcessExit != 0 || v.Status != "succeeded" ||
		!operationStackPattern.MatchString(want.Stack) || !preflightAction(want.Operation) || want.Engine != "docker" ||
		!operationDigestPattern.MatchString(want.EndpointDigest) || !operationDigestPattern.MatchString(want.QualifiedIdentityDigest) {
		return ErrPreflightBinding
	}
	r := v.Result
	// Revalidate exported/mutable DTOs rather than trusting previous decoding.
	if !validPreflightResult(r) || r.Outcome != "succeeded" || r.Stack != want.Stack || r.Operation != want.Operation ||
		r.Target == nil || r.Target.Engine != want.Engine || r.Target.EndpointDigest != want.EndpointDigest || r.Target.QualifiedIdentityDigest != want.QualifiedIdentityDigest {
		return ErrPreflightBinding
	}
	return nil
}

func preflightAction(s string) bool { return oneOf(s, "up", "start", "restart", "stop", "down") }
