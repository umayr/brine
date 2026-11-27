package secretbroker

import (
	"database/sql"
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
	"strings"
	"time"
)

type DeploymentIntent struct {
	ID                                        string
	Identity                                  ReviewIdentity
	Reference                                 brokerwire.Reference
	StageID, JournalID, SelectionDigest, Mode string
	Revisions                                 []Revision
	Retirement                                *brokerwire.Retirement `json:",omitempty"`
}
type DeploymentEvent struct {
	Sequence                    int
	State, Effects, OperationID string
	At                          time.Time
}
type DeploymentRecord struct {
	Intent DeploymentIntent
	At     time.Time
	Events []DeploymentEvent
}

func (i DeploymentIntent) Valid() bool {
	s := i.Reference.Scope
	if !validID(i.ID) || !validReview(i.Identity) || !i.Reference.Valid() || !validID(i.StageID) || !validID(i.JournalID) || !validDigest(i.SelectionDigest) || (i.Mode != "create" && i.Mode != "update") || len(i.Revisions) > 128 {
		return false
	}
	if s.Registration != i.Identity.Scope.RegistrationID || s.Stack != i.Identity.Scope.StackName || s.SourceDigest != i.Identity.SourceDigest || s.TargetDigest != i.Identity.TargetDigest || s.PolicyRevision != i.Identity.PolicyRevision || s.BindingRevision != uint64(i.Identity.BindingRevision) {
		return false
	}
	if i.Retirement != nil && (!i.Retirement.Valid() || i.Retirement.Scope != s || len(i.Revisions) != 0) {
		return false
	}
	for _, r := range i.Revisions {
		if !token(r.Name, 128, "_.-") || !token(r.Connection, 128, "_.-") || len(r.ProviderRevision) > 128 || strings.ContainsAny(r.ProviderRevision, "\x00\r\n") {
			return false
		}
	}
	return true
}
func (e DeploymentEvent) Valid() bool {
	if e.Sequence < 0 || e.Sequence > 31 {
		return false
	}
	if e.OperationID != "" && (!strings.HasPrefix(e.OperationID, "op_") || !validID(strings.TrimPrefix(e.OperationID, "op_"))) {
		return false
	}
	switch e.State {
	case "pending":
		return e.Sequence == 0 && e.Effects == "unknown" && e.OperationID == ""
	case "unknown", "running":
		return e.Sequence > 0 && e.Effects == "unknown" && e.OperationID == ""
	case "not-admitted":
		return e.Sequence > 0 && e.Effects == "none" && e.OperationID == ""
	case "succeeded":
		return e.Sequence > 0 && (e.Effects == "complete" || e.Effects == "none") && e.OperationID != ""
	case "failed":
		return e.Sequence > 0 && (e.Effects == "none" || e.Effects == "partial" && e.OperationID != "")
	}
	return false
}

// BeginDeployment is immutable intent. Call BEFORE WithCurrent, never inside
// its revision transaction. Failure means no target dispatch is permitted.
func (s *Store) BeginDeployment(i DeploymentIntent) error {
	if !i.Valid() {
		return ErrInvalidInput
	}
	raw, err := json.Marshal(i)
	if err != nil || len(raw) > 65536 {
		return ErrInvalidInput
	}
	return s.withTx(func(tx *sql.Tx) error {
		if checkScope(tx, i.Identity.Scope) != nil {
			return ErrScopeMismatch
		}
		var n int
		if tx.QueryRow("SELECT count(*) FROM broker_deployments").Scan(&n) != nil {
			return ErrUnavailable
		}
		if n >= 4096 {
			return ErrCapacity
		}
		if _, err := tx.Exec("INSERT INTO broker_deployments(id,registration_id,payload,at_ns) VALUES(?,?,?,?)", i.ID, i.Identity.Scope.RegistrationID, string(raw), time.Now().UnixNano()); err != nil {
			return ErrConflict
		}
		_, err := tx.Exec("INSERT INTO broker_deployment_events VALUES(?,0,'pending','unknown','',?)", i.ID, time.Now().UnixNano())
		if err != nil {
			return ErrUnavailable
		}
		return nil
	})
}

// AppendDeploymentEvent appends, never overwrites prior evidence. Terminal
// success/failed/not-admitted cannot be rewritten into a different outcome.
func (s *Store) AppendDeploymentEvent(scope Scope, id string, expectedSequence int, event DeploymentEvent) error {
	if !validScope(scope) || !validID(id) || event.Sequence != expectedSequence+1 || !event.Valid() {
		return ErrInvalidInput
	}
	return s.withTx(func(tx *sql.Tx) error {
		if checkScope(tx, scope) != nil {
			return ErrScopeMismatch
		}
		var reg, state string
		var seq int
		if tx.QueryRow("SELECT registration_id FROM broker_deployments WHERE id=?", id).Scan(&reg) != nil || reg != scope.RegistrationID {
			return ErrScopeMismatch
		}
		if tx.QueryRow("SELECT sequence,state FROM broker_deployment_events WHERE id=? ORDER BY sequence DESC LIMIT 1", id).Scan(&seq, &state) != nil {
			return ErrUnavailable
		}
		if seq != expectedSequence || state == "succeeded" || state == "failed" || state == "not-admitted" {
			return ErrConflict
		}
		_, err := tx.Exec("INSERT INTO broker_deployment_events VALUES(?,?,?,?,?,?)", id, event.Sequence, event.State, event.Effects, event.OperationID, time.Now().UnixNano())
		if err != nil {
			return ErrUnavailable
		}
		return nil
	})
}
func (s *Store) Deployments(scope Scope) ([]DeploymentRecord, error) {
	if !validScope(scope) {
		return nil, ErrInvalidInput
	}
	out := []DeploymentRecord{}
	err := s.withTx(func(tx *sql.Tx) error {
		if err := checkScope(tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT payload,at_ns FROM broker_deployments WHERE registration_id=? ORDER BY at_ns DESC", scope.RegistrationID)
		if err != nil {
			return ErrUnavailable
		}
		for rows.Next() {
			var r DeploymentRecord
			var raw string
			var ns int64
			if rows.Scan(&raw, &ns) != nil || len(raw) > 65536 || json.Unmarshal([]byte(raw), &r.Intent) != nil || !r.Intent.Valid() || r.Intent.Identity.Scope != scope || ns <= 0 || len(out) >= 4096 {
				rows.Close()
				return ErrUnavailable
			}
			r.At = time.Unix(0, ns).UTC()
			out = append(out, r)
		}
		if rows.Err() != nil {
			rows.Close()
			return ErrUnavailable
		}
		rows.Close()
		for i := range out {
			events, err := tx.Query("SELECT sequence,state,effects,operation_id,at_ns FROM broker_deployment_events WHERE id=? ORDER BY sequence", out[i].Intent.ID)
			if err != nil {
				return ErrUnavailable
			}
			for events.Next() {
				var e DeploymentEvent
				var ns int64
				if events.Scan(&e.Sequence, &e.State, &e.Effects, &e.OperationID, &ns) != nil || e.Sequence != len(out[i].Events) || !e.Valid() || ns <= 0 {
					events.Close()
					return ErrUnavailable
				}
				e.At = time.Unix(0, ns).UTC()
				out[i].Events = append(out[i].Events, e)
			}
			if events.Err() != nil {
				events.Close()
				return ErrUnavailable
			}
			events.Close()
			if len(out[i].Events) == 0 {
				return ErrUnavailable
			}
		}
		return nil
	})
	return out, err
}
