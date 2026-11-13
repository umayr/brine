package dill

import (
	"encoding/json"
	"errors"
)

const OperationVersion = "dill.operation/1"
const OperationMarker = "[brine:operation] "

// QualifiedOperationConfig is an operator-provisioned assertion, not readiness.
// DaemonID MUST be independently provisioned, never learned from the selected socket.
// Layout attests native same-account Linux Docker callers and qualified local storage.
type QualifiedOperationConfig struct{ StateDir, Layout, DaemonID string }

type OperationTarget struct {
	Engine                  string `json:"engine"`
	EndpointDigest          string `json:"endpointDigest"`
	QualifiedIdentityDigest string `json:"qualifiedIdentityDigest,omitempty"`
}
type OperationResource struct {
	Kind           string `json:"kind"`
	IdentityDigest string `json:"identityDigest"`
	Action         string `json:"action"`
	Effect         string `json:"effect"`
}
type OperationDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// OperationReceipt retains opaque correlation tokens; it is not a resource inventory,
// saved-plan authorization, raw diff or claim of transactional execution.
type OperationReceipt struct {
	Version            string                `json:"version"`
	OperationID        string                `json:"operationId"`
	Operation          string                `json:"operation"`
	Target             *OperationTarget      `json:"target"`
	Stack              string                `json:"stack"`
	Outcome            string                `json:"outcome"`
	Effects            string                `json:"effects"`
	ChangesDetected    bool                  `json:"changesDetected"`
	Resources          []OperationResource   `json:"resources"`
	ResourcesTruncated bool                  `json:"resourcesTruncated"`
	Errors             []OperationDiagnostic `json:"errors"`
}

// OperationInvocation keeps process success independent from observed engine axes.
// A nil receipt means unknown outcome AND effects. Status is never inferred from
// outcome alone: postcommit release failure can be succeeded/complete with exit 1.
type OperationInvocation struct {
	Receipt  *OperationReceipt `json:"receipt"`
	ExitCode int               `json:"exitCode"`
	Status   string            `json:"status"`
}

type OperationError struct{ Invocation OperationInvocation }

func (e *OperationError) Error() string {
	return "Dill operation unavailable or incomplete; inspect validated receipt and target before any new attempt"
}

var ErrOperationContract = errors.New("Dill operation contract unavailable or invalid")
var ErrQualifiedOperationConfig = errors.New("qualified operations require independently provisioned native Linux Docker and shared private state")

// DecodeOperationInvocation validates a marker payload, not its provenance. Browser
// supplied markers remain untrusted; only the subprocess adapter binds requests.
func DecodeOperationInvocation(data []byte) (*OperationInvocation, error) {
	var v OperationInvocation
	m, err := operationObject(data, []string{"receipt", "exitCode", "status"}, nil)
	if err != nil {
		return nil, ErrOperationContract
	}
	if string(m["exitCode"]) == "null" || json.Unmarshal(m["exitCode"], &v.ExitCode) != nil || json.Unmarshal(m["status"], &v.Status) != nil {
		return nil, ErrOperationContract
	}
	if string(m["receipt"]) != "null" {
		v.Receipt, err = parseOperationReceipt(m["receipt"])
		if err != nil {
			return nil, ErrOperationContract
		}
	}
	if v.ExitCode < -1 || v.ExitCode > 255 {
		return nil, ErrOperationContract
	}
	if v.Receipt == nil {
		if v.Status != "unknown" {
			return nil, ErrOperationContract
		}
	} else if v.ExitCode < 0 || invocationStatus(v.Receipt, v.ExitCode) != v.Status {
		return nil, ErrOperationContract
	}
	return &v, nil
}
