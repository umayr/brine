// Package planstate detects changes to Brine-managed inputs since a displayed
// plan. A matching tag is not deployment authority or a saved executable plan:
// target state, external writers and Pkl dependencies may still change.
package planstate

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/store"
)

const Header = "X-Brine-Plan-Inputs"

var key = func() [32]byte { var b [32]byte; rand.Read(b[:]); return b }()
var ErrUnavailable = errors.New("plan inputs unavailable")

// Brine editor, entrypoint, Git and revision publications update the registration.
// Include effective variable values separately: even shared edits can affect it.
func Tag(stack *store.Stack, vars *configvars.Store) (string, error) {
	if stack == nil || vars == nil {
		return "", ErrUnavailable
	}
	values, err := vars.Effective(stack.Name)
	if err != nil {
		return "", ErrUnavailable
	}
	mac := hmac.New(sha256.New, key[:])
	if err := json.NewEncoder(mac).Encode([]any{stack, values}); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}
