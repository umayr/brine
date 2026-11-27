package secretbroker

import (
	"bytes"
	"database/sql"
	"encoding/json"
)

func verifyDeploymentState(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT id,registration_id,payload,at_ns FROM broker_deployments")
	if err != nil {
		return ErrUnavailable
	}
	intents := map[string]DeploymentIntent{}
	for rows.Next() {
		var id, reg, raw string
		var ns int64
		var i DeploymentIntent
		if rows.Scan(&id, &reg, &raw, &ns) != nil || len(raw) > 65536 || json.Unmarshal([]byte(raw), &i) != nil || !i.Valid() || i.ID != id || i.Identity.Scope.RegistrationID != reg || ns <= 0 || len(intents) >= 4096 {
			rows.Close()
			return ErrUnavailable
		}
		canonical, err := json.Marshal(i)
		if err != nil || !bytes.Equal(canonical, []byte(raw)) {
			rows.Close()
			return ErrUnavailable
		}
		intents[id] = i
	}
	if rows.Err() != nil {
		rows.Close()
		return ErrUnavailable
	}
	rows.Close()
	for id, i := range intents {
		if checkScope(tx, i.Identity.Scope) != nil {
			return ErrUnavailable
		}
		es, err := tx.Query("SELECT sequence,state,effects,operation_id,at_ns FROM broker_deployment_events WHERE id=? ORDER BY sequence", id)
		if err != nil {
			return ErrUnavailable
		}
		seq := 0
		terminal := false
		for es.Next() {
			var e DeploymentEvent
			var ns int64
			if es.Scan(&e.Sequence, &e.State, &e.Effects, &e.OperationID, &ns) != nil || terminal || e.Sequence != seq || !e.Valid() || ns <= 0 {
				es.Close()
				return ErrUnavailable
			}
			terminal = e.State == "succeeded" || e.State == "failed" || e.State == "not-admitted"
			seq++
		}
		if es.Err() != nil || seq == 0 {
			es.Close()
			return ErrUnavailable
		}
		es.Close()
	}
	return nil
}
