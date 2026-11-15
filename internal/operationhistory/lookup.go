package operationhistory

import (
	"encoding/json"
	"os"
)

// LookupRecord is an in-process discriminated union, not a persisted or wire
// schema. Exactly one version-specific record is non-nil on successful lookup.
type LookupRecord struct {
	Version string
	V1      *Record
	V2      *RecordV2
}

// Lookup dispatches once on the record's explicit version and then performs
// that version's complete closed canonical validation. HTTP authentication and
// serialization of the selected original record are the caller's responsibility.
func (s *Store) Lookup(id string) (LookupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return LookupRecord{}, os.ErrNotExist
	}
	if r, ok := s.known[id]; ok {
		v := clone(r)
		return LookupRecord{Version: Version, V1: &v}, nil
	}
	if r, ok := s.knownV2[id]; ok {
		v := cloneV2(r)
		return LookupRecord{Version: VersionV2, V2: &v}, nil
	}
	b, e := s.disk.read(id + ".json")
	if e != nil {
		return LookupRecord{}, e
	}
	if len(b) > MaxRecord {
		return LookupRecord{}, ErrUnsafe
	}
	var header struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &header) != nil {
		return LookupRecord{}, ErrUnsafe
	}
	switch header.Version {
	case Version:
		r, e := decode(b)
		if e != nil || r.RequestID != id {
			return LookupRecord{}, ErrUnsafe
		}
		return LookupRecord{Version: Version, V1: &r}, nil
	case VersionV2:
		r, e := decodeV2(b)
		if e != nil || r.RequestID != id || (r.Kind == "apply" && s.validateReviewLink(r.Intent, r.PreviewRequestID, r.ReviewDigest) != nil) {
			return LookupRecord{}, ErrUnsafe
		}
		return LookupRecord{Version: VersionV2, V2: &r}, nil
	default:
		return LookupRecord{}, ErrUnsafe
	}
}
