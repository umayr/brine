package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ManagedFields struct {
	Username, Password, APIToken bool
}

// CredentialFields returns ownership flags only, never credential values.
func (a *Authenticator) CredentialFields() ManagedFields {
	if a == nil || a.management == nil {
		return ManagedFields{}
	}
	return a.management.fields
}

type credentialManagement struct {
	dir       string
	fields    ManagedFields
	digest    [sha256.Size]byte // protected by Authenticator.mu
	uncertain bool              // refuse another write after a failed durability check
}

type CredentialAction string

const (
	UpdateSignIn       CredentialAction = "update"
	ShowAPIToken       CredentialAction = "show"
	RegenerateAPIToken CredentialAction = "regenerate"
)

type CredentialChange struct {
	Action          CredentialAction `json:"action"`
	CurrentPassword string           `json:"current_password"`
	Username        *string          `json:"username,omitempty"`
	NewPassword     string           `json:"new_password,omitempty"`
}

type CredentialResult struct {
	SignedOut           bool   `json:"signed_out,omitempty"`
	Token               string `json:"token,omitempty"`
	DurabilityUncertain bool   `json:"durability_uncertain,omitempty"`
}

var (
	ErrCredentialSession     = errors.New("a current browser session is required")
	ErrCredentialManaged     = errors.New("credential is managed by deployment")
	ErrCredentialInput       = errors.New("invalid credential change")
	ErrCredentialNoChange    = errors.New("no sign-in changes to update")
	ErrCredentialPassword    = errors.New("current password is incorrect")
	ErrCredentialRate        = errors.New("too many attempts; try again later")
	ErrCredentialBusy        = errors.New("credential verification is busy; try again later")
	ErrCredentialChanged     = errors.New("credentials changed; reload the page and try again")
	ErrCredentialUnavailable = errors.New("credential file is unavailable or changed outside Brine; inspect it before retrying")
	errCredentialDurability  = errors.New("credentials published, but directory sync failed")
	errLoginChanged          = errors.New("login changed before session creation")
)

// ManageCredentials is only available to a live cookie session admitted by
// auth middleware (including CSRF). Verification shares the login work budget.
func (a *Authenticator) ManageCredentials(r *http.Request, change CredentialChange) (CredentialResult, error) {
	var result CredentialResult
	if !a.Enabled() || !CookieAuthenticated(r) || r.Method != http.MethodPost {
		return result, ErrCredentialSession
	}
	m := a.management
	if m == nil {
		return result, ErrCredentialManaged
	}
	if change.CurrentPassword == "" {
		return result, ErrCredentialInput
	}
	switch change.Action {
	case UpdateSignIn:
		if (change.Username != nil && !m.fields.Username) || (change.NewPassword != "" && !m.fields.Password) || (!m.fields.Username && !m.fields.Password) {
			return result, ErrCredentialManaged
		}
		if change.Username != nil {
			user := *change.Username
			if user == "" || len(user) > 128 || user != strings.TrimSpace(user) || strings.ContainsFunc(user, unicode.IsControl) {
				return result, ErrCredentialInput
			}
		}
		if p := change.NewPassword; p != "" && (len(p) > 1024 || utf8.RuneCountInString(p) < 15 || strings.TrimSpace(p) == "" || strings.ContainsRune(p, 0)) {
			return result, ErrCredentialInput
		}
	case ShowAPIToken, RegenerateAPIToken:
		if !m.fields.APIToken {
			return result, ErrCredentialManaged
		}
		if change.Username != nil || change.NewPassword != "" {
			return result, ErrCredentialInput
		}
	default:
		return result, ErrCredentialInput
	}

	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return result, ErrCredentialSession
	}
	a.mu.Lock()
	sess := a.sessions[cookie.Value]
	validSession := sess != nil && a.now().Before(sess.expires) && r.Context().Err() == nil &&
		subtle.ConstantTimeCompare([]byte(a.CSRFFor(r)), []byte(sess.csrf)) == 1
	login := a.login
	digest := m.digest
	a.mu.Unlock()
	if !validSession {
		return result, ErrCredentialSession
	}

	next := login
	if change.Username != nil {
		next.user = *change.Username
	}
	passwordChanged := change.NewPassword != "" && change.NewPassword != change.CurrentPassword
	if err := func() error {
		select {
		case a.derivations <- struct{}{}:
			defer func() { <-a.derivations }()
		default:
			return ErrCredentialBusy
		}
		if !a.allowAttempt(clientIP(r)) {
			return ErrCredentialRate
		}
		if !login.verify(sess.user, change.CurrentPassword) {
			return ErrCredentialPassword
		}
		if change.Action == UpdateSignIn {
			if !passwordChanged && next.user == login.user {
				return ErrCredentialNoChange
			}
			if passwordChanged && next.setPassword(change.NewPassword) != nil {
				return ErrCredentialUnavailable
			}
		}
		return nil
	}(); err != nil {
		return result, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[cookie.Value] != sess || !a.now().Before(sess.expires) || r.Context().Err() != nil {
		return result, ErrCredentialSession
	}
	if a.login.revision != login.revision || m.digest != digest {
		return result, ErrCredentialChanged
	}
	if m.uncertain && change.Action != ShowAPIToken {
		return result, ErrCredentialUnavailable
	}
	state, err := lockCredentialState(m.dir, false)
	if err != nil {
		return result, ErrCredentialUnavailable
	}
	defer state.close()
	data, exists, err := state.read()
	var stored Credentials
	if err != nil || !exists || sha256.Sum256(data) != digest || json.Unmarshal(data, &stored) != nil {
		return result, ErrCredentialUnavailable
	}
	if change.Action == ShowAPIToken {
		result.Token = stored.APIToken
		result.DurabilityUncertain = m.uncertain
		return result, nil
	}
	if change.Action == RegenerateAPIToken {
		stored.APIToken, err = randomSecret(32)
		if err != nil {
			return result, ErrCredentialUnavailable
		}
	} else {
		// Preserve shadowed fields, including any file-backed fallback an
		// operator will get if they remove a startup override later.
		if change.Username != nil {
			stored.User = next.user
		}
		if passwordChanged {
			stored.Password = change.NewPassword
		}
	}
	data, err = encodeCredentials(stored)
	if err != nil {
		return result, ErrCredentialUnavailable
	}
	err = state.write(data)
	if err != nil && !errors.Is(err, errCredentialDurability) {
		return result, ErrCredentialUnavailable
	}
	// Rename is the publication point. Match the published value in memory
	// even if directory fsync failed; report uncertain durability, not rollback.
	m.digest = sha256.Sum256(data)
	m.uncertain = err != nil
	result.DurabilityUncertain = m.uncertain
	if change.Action == RegenerateAPIToken {
		a.tokenHash = sha256.Sum256([]byte(stored.APIToken))
		result.Token = stored.APIToken
	} else {
		next.revision++
		a.login = next
		for id, session := range a.sessions {
			delete(a.sessions, id)
			close(session.done)
		}
		result.SignedOut = true
	}
	return result, nil
}
