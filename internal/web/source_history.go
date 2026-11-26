package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/store"
)

type revisionArchive interface {
	PutChain(context.Context, string, []string, []snapshothistory.SourceRevision) ([]string, error)
	Get(string, string) (snapshothistory.SourceRevision, error)
	Usage(context.Context) (snapshothistory.SourceArchiveUsage, error)
}
type sourceHistoryAccess struct {
	vault     revisionArchive
	workspace store.RevisionWorkspace
	reviews   [32]byte
}

func (h *Handler) EnableSourceHistory(archive *snapshothistory.SourceArchive) error {
	ws, ok := h.store.(store.RevisionWorkspace)
	if archive == nil || !ok || h.auth == nil || !h.auth.Enabled() || h.agent == nil || h.agent.ID() != "local" || h.variableModeDisabled() || h.stagingStore != nil || h.remoteEnabled {
		return store.ErrSourceUnavailable
	}
	run := h.agent.Dill()
	if !filepath.IsAbs(run.Socket) || filepath.Clean(run.Socket) != run.Socket || run.BeforeLegacyAction == nil || run.CheckSourceProtection == nil || run.Coordinator.CheckAdmission == nil {
		return store.ErrSourceUnavailable
	}
	access := &sourceHistoryAccess{vault: archive, workspace: ws}
	if _, e := rand.Read(access.reviews[:]); e != nil {
		return store.ErrSourceUnavailable
	}
	h.sourceHistory = access
	return nil
}

// Browser-only authority. Middleware verifies CSRF on every POST; neither a
// bearer token nor --no-auth grants archived-content or restore authority.
func (h *Handler) historySession(w http.ResponseWriter, r *http.Request, submit bool) (*http.Request, func(), bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	if h.auth == nil || !auth.CookieAuthenticated(r) {
		http.Error(w, "An interactive admin session is required for stack history.", http.StatusForbidden)
		return r, nil, false
	}
	var ctx context.Context
	var release func()
	var ok bool
	if submit {
		ctx, release, ok = h.auth.SubmissionLease(r)
	} else {
		ctx, release, ok = h.auth.SessionContext(r)
	}
	if !ok {
		http.Error(w, "Session expired", http.StatusForbidden)
		return r, nil, false
	}
	return r.WithContext(ctx), release, true
}

type requestEvidence struct {
	Mode, ID, Host, StatusURL string
	Resolved                  bool
}
type revisionRow struct {
	Number, From, Count int
	ID, Actor, When     string
}
type sourceDiff struct {
	Path, Change, Unavailable string
	BeforeBytes, AfterBytes   int
	Lines                     []sourceLine
}
type historyPage struct {
	Stack, CSRF, Message, Review, ID, Proof               string
	Rows                                                  []revisionRow
	Requests                                              []requestEvidence
	RequestError, ShowRequests, Enabled, Compare, Confirm bool
	AutoCheckpoint                                        bool
	From, Previous, Page, Next, Prev, Total               int
	Files                                                 []sourceDiff
	EntryBefore, EntryAfter                               string
	Capacity                                              *snapshothistory.SourceArchiveUsage
	SlotsNeeded                                           int
}

func (h *Handler) manualRows(name string) ([]requestEvidence, bool, error) {
	reader, ok := h.store.(interface {
		ExistingManualIntents() ([]store.ManualDeploymentIntent, error)
	})
	if !ok {
		return nil, false, store.ErrManualIntent
	}
	rows, err := reader.ExistingManualIntents()
	if err != nil {
		return nil, false, err
	}
	out := []requestEvidence{}
	pending := false
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if row.Stack != name {
			continue
		}
		link := ""
		if h.remoteEnabled {
			link = "/remote/deployment?" + url.Values{"host": {row.Host}, "id": {row.Request}}.Encode()
		}
		out = append(out, requestEvidence{row.Mode, row.Request, row.Host, link, row.Resolved})
		pending = pending || !row.Resolved
	}
	return out, pending, nil
}
func (h *Handler) sourceEligible(r *http.Request, st *store.Stack, pending bool) bool {
	if h.sourceHistory == nil || st == nil || st.Git != nil || st.RemoteGit != nil || pending || h.remoteEnabled || h.stagingStore != nil || h.variableModeDisabled() {
		return false
	}
	run := h.agent.Dill()
	return run.CheckSourceProtection != nil && run.CheckSourceProtection(st.Name) == nil && run.BeforeLegacyAction != nil && run.Coordinator.CheckAdmission != nil && run.BeforeLegacyAction(r.Context(), st.PklPath) == nil
}
func (h *Handler) stackHistory(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if store.ValidateStackName(name) != nil {
		http.NotFound(w, r)
		return
	}
	p := historyPage{Stack: name}
	// In a local no-auth workspace explain the capability without exposing any archive metadata.
	if !auth.CookieAuthenticated(r) {
		w.Header().Set("Cache-Control", "no-store")
		p.Message = "Source history and restore require an authenticated browser session and an explicitly configured archive."
		h.renderPartial(w, r, "stack-history", p)
		return
	}
	r, end, ok := h.historySession(w, r, false)
	if !ok {
		return
	}
	defer end()
	unlock, ok := h.admitStack(w, r, name)
	if !ok {
		return
	}
	defer unlock()
	st, e := h.store.Get(name)
	if e != nil {
		http.Error(w, "Stack history unavailable", 409)
		return
	}
	p.CSRF = h.auth.CSRFFor(r)
	var pending bool
	p.Requests, pending, e = h.manualRows(name)
	p.RequestError = e != nil
	p.ShowRequests = len(p.Requests) > 0 || (h.remoteEnabled && p.RequestError)
	switch {
	case h.sourceHistory == nil:
		p.Message = "Source revisions are not enabled. Restore requires a private source archive; a deployment receipt alone cannot restore files."
	case st.Git != nil:
		p.Message = "Git manages this source. Review a revert in the upstream repository; Restore cannot overwrite a checkout."
	case pending || p.RequestError:
		p.Message = "Source changes blocked: the request ledger is unavailable or an original request remains unresolved."
	case !h.sourceEligible(r, st, pending):
		p.Message = "Source history unavailable. Workspace eligibility or target ownership could not be verified."
	default:
		view, err := h.sourceHistory.workspace.ReviewSource(r.Context(), name)
		if err != nil {
			p.Message = "Source history is unavailable for this workspace. Only private managed Pkl projects with no extra files, links or mounts are eligible."
			break
		}
		if usage, err := h.sourceHistory.vault.Usage(r.Context()); err == nil {
			p.Capacity = &usage
		}
		p.Total = len(view.Revisions)
		p.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
		if p.Page < 1 {
			p.Page = 1
		}
		if p.Page > max(1, (p.Total+9)/10) {
			http.Error(w, "Unknown history page", 400)
			return
		}
		if p.Page > 1 {
			p.Prev = p.Page - 1
		}
		if p.Page*10 < p.Total {
			p.Next = p.Page + 1
		}
		p.Enabled = true
		p.AutoCheckpoint = st.AutoCheckpoint
		for i := p.Total - 1 - 10*(p.Page-1); i >= max(0, p.Total-10*p.Page); i-- {
			if r.Context().Err() != nil {
				http.Error(w, "Session or request expired", 403)
				return
			}
			row, err := h.sourceHistory.record(view, i)
			if err != nil {
				p.Enabled = false
				p.Rows = nil
				p.Message = "Retained revision history could not be verified. No restore is available."
				break
			}
			p.Rows = append(p.Rows, revisionRow{i + 1, slices.Index(view.Revisions, row.RestoredFrom) + 1, len(row.Files), view.Revisions[i], row.Actor, row.Created.UTC().Format("2006-01-02 15:04:05 UTC")})
		}
	}
	if r.Context().Err() != nil {
		http.Error(w, "Session or request expired", 403)
		return
	}
	h.renderPartial(w, r, "stack-history", p)
}
func (s *sourceHistoryAccess) record(v store.SourceView, i int) (snapshothistory.SourceRevision, error) {
	if i < 0 || i >= len(v.Revisions) {
		return snapshothistory.SourceRevision{}, store.ErrSourceUnavailable
	}
	record, e := s.vault.Get(v.Scope, v.Revisions[i])
	parent := ""
	if i > 0 {
		parent = v.Revisions[i-1]
	}
	if e != nil || record.Parent != parent || (record.RestoredFrom != "" && !slices.Contains(v.Revisions[:i], record.RestoredFrom)) {
		return snapshothistory.SourceRevision{}, store.ErrSourceUnavailable
	}
	return record, nil
}
func (s *sourceHistoryAccess) proof(session, name, kind, id, base string, expires int64) string {
	b, _ := json.Marshal([]string{session, name, kind, id, base, strconv.FormatInt(expires, 10)})
	mac := hmac.New(sha256.New, s.reviews[:])
	mac.Write(b)
	return fmt.Sprintf("%d.%x", expires, mac.Sum(nil))
}
func (s *sourceHistoryAccess) verifyProof(proof, session, name, kind, id, base string) bool {
	stamp, _, ok := strings.Cut(proof, ".")
	until, e := strconv.ParseInt(stamp, 10, 64)
	now := time.Now().Unix()
	return ok && e == nil && until >= now && until <= now+300 && hmac.Equal([]byte(proof), []byte(s.proof(session, name, kind, id, base, until)))
}

type sourceLine struct {
	Kind         string
	Prefix       string
	Text         string
	Unterminated bool
}

const sourceDiffFiles = 8
const sourceDiffLines = 256
const sourceDiffBytes = 16 << 10

// Bound both the rendered input and the quadratic LCS table. Always keep
// file-level change status, even for files too large for an inline text diff.
func sourceLines(a, b []byte) ([]sourceLine, string) {
	if len(a) > sourceDiffBytes || len(b) > sourceDiffBytes {
		return nil, "File is too large for an inline diff."
	}
	if !utf8.Valid(a) || !utf8.Valid(b) || bytes.ContainsRune(a, 0) || bytes.ContainsRune(b, 0) {
		return nil, "This file is not text."
	}
	split := func(raw []byte) []string {
		s := strings.SplitAfter(string(raw), "\n")
		if s[len(s)-1] == "" {
			s = s[:len(s)-1]
		}
		return s
	}
	old, next := split(a), split(b)
	if len(old) > sourceDiffLines || len(next) > sourceDiffLines {
		return nil, "File has too many lines for an inline diff."
	}
	width := len(next) + 1
	sub := make([]uint16, (len(old)+1)*width)
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(next) - 1; j >= 0; j-- {
			if old[i] == next[j] {
				sub[i*width+j] = 1 + sub[(i+1)*width+j+1]
			} else {
				sub[i*width+j] = max(sub[(i+1)*width+j], sub[i*width+j+1])
			}
		}
	}
	add := func(lines []sourceLine, text, kind, prefix string) []sourceLine {
		return append(lines, sourceLine{kind, prefix, strings.TrimSuffix(text, "\n"), !strings.HasSuffix(text, "\n")})
	}
	out := make([]sourceLine, 0, len(old)+len(next))
	for i, j := 0, 0; i < len(old) || j < len(next); {
		switch {
		case i < len(old) && j < len(next) && old[i] == next[j]:
			out = add(out, old[i], "same", " ")
			i++
			j++
		case i < len(old) && (j == len(next) || sub[(i+1)*width+j] >= sub[i*width+j+1]):
			out = add(out, old[i], "remove", "-")
			i++
		default:
			out = add(out, next[j], "add", "+")
			j++
		}
	}
	return out, ""
}
func diffSource(current, candidate map[string][]byte, compare bool) []sourceDiff {
	all := map[string]bool{}
	for k := range current {
		all[k] = true
	}
	for k := range candidate {
		all[k] = true
	}
	var rows []sourceDiff
	shown := 0
	for _, k := range slices.Sorted(maps.Keys(all)) {
		a, hasA := current[k]
		b, hasB := candidate[k]
		d := sourceDiff{Path: k, BeforeBytes: len(a), AfterBytes: len(b)}
		switch {
		case !hasA:
			d.Change = "Add"
		case !hasB:
			d.Change = "Remove"
		case !bytes.Equal(a, b):
			d.Change = "Change"
		default:
			d.Change = "Unchanged"
		}
		if compare && d.Change != "Unchanged" {
			if shown < sourceDiffFiles {
				d.Lines, d.Unavailable = sourceLines(a, b)
				shown++
			} else {
				d.Unavailable = "Inline diff omitted after the first eight changed files."
			}
		}
		rows = append(rows, d)
	}
	return rows
}

func (h *Handler) stackSourceReview(w http.ResponseWriter, r *http.Request) {
	h.sourcePost(w, r, false)
}
func (h *Handler) stackSourceConfirm(w http.ResponseWriter, r *http.Request) {
	h.sourcePost(w, r, true)
}
func (h *Handler) sourcePost(w http.ResponseWriter, r *http.Request, confirm bool) {
	r, end, ok := h.historySession(w, r, confirm)
	if !ok {
		return
	}
	defer end()
	name := chi.URLParam(r, "name")
	if h.sourceHistory == nil || store.ValidateStackName(name) != nil {
		http.Error(w, "Source restore is not enabled", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid review form", 400)
		return
	}
	kind, id := r.PostForm.Get("kind"), r.PostForm.Get("revision")
	if (kind != "retain" && kind != "restore" && kind != "diff") ||
		(kind == "retain" && id != "") || (kind != "retain" && len(id) != 64) || (confirm && kind == "diff") {
		http.Error(w, "Invalid revision selection", 400)
		return
	}
	unlock, ok := h.admitStack(w, r, name)
	if !ok {
		return
	}
	defer unlock()
	st, err := h.store.Get(name)
	if err != nil {
		http.Error(w, "Stack unavailable; no change made", 409)
		return
	}
	_, pending, err := h.manualRows(name)
	if err != nil || !h.sourceEligible(r, st, pending) {
		http.Error(w, "Source change blocked. Check workspace ownership and unresolved requests.", 409)
		return
	}
	s := h.sourceHistory
	v, err := s.workspace.ReviewSource(r.Context(), name)
	if err != nil {
		http.Error(w, "Current source could not be verified. No change made.", 409)
		return
	}
	csrf := h.auth.CSRFFor(r)
	p := historyPage{Stack: name, CSRF: csrf, Review: kind, ID: id, EntryBefore: v.Entrypoint, EntryAfter: v.Entrypoint, Confirm: true, Compare: r.PostForm.Get("compare") == "yes"}
	var candidate snapshothistory.SourceRevision
	if kind == "diff" {
		i := slices.Index(v.Revisions, id)
		candidate, err = s.record(v, i)
		if err != nil {
			http.Error(w, "Revision not retained or unverifiable for this stack", 409)
			return
		}
		var previous snapshothistory.SourceRevision
		if i > 0 {
			previous, err = s.record(v, i-1)
			if err != nil {
				http.Error(w, "Previous revision unavailable", 409)
				return
			}
		}
		p.From, p.Previous = i+1, i
		p.EntryBefore, p.EntryAfter = previous.Entrypoint, candidate.Entrypoint
		p.Files, p.Confirm, p.Compare = diffSource(previous.Files, candidate.Files, true), false, true
		if r.Context().Err() != nil {
			http.Error(w, "Session or request expired", 403)
			return
		}
		h.renderPartial(w, r, "stack-history", p)
		return
	}
	if kind == "restore" {
		i := slices.Index(v.Revisions, id)
		candidate, err = s.record(v, i)
		if err != nil {
			http.Error(w, "Revision not retained or unverifiable for this stack", 409)
			return
		}
		p.From = i + 1
		p.EntryAfter = candidate.Entrypoint
		p.Files = diffSource(v.Files, candidate.Files, p.Compare)
		if p.EntryAfter == v.Entrypoint && maps.EqualFunc(v.Files, candidate.Files, bytes.Equal) {
			p.Confirm = false
			p.Message = "This revision already matches the current source."
		}
	} else {
		p.Files = diffSource(nil, v.Files, false)
		_, changed, err := s.checkpointHead(v)
		if err != nil {
			http.Error(w, "Previous revision unavailable", 409)
			return
		}
		if !changed {
			p.Confirm = false
			p.Message = "These exact source files are already retained as the latest revision."
		}
	}
	if r.Context().Err() != nil {
		http.Error(w, "Session or request expired", 403)
		return
	}
	if !confirm {
		if p.Confirm {
			p.SlotsNeeded = 1
			if kind == "restore" {
				_, changed, err := s.checkpointHead(v)
				if err != nil {
					http.Error(w, "Previous revision unavailable", 409)
					return
				}
				if changed {
					p.SlotsNeeded++
				}
			}
			if usage, err := s.vault.Usage(r.Context()); err == nil {
				p.Capacity = &usage
				if p.SlotsNeeded > usage.RemainingObjects() || usage.RemainingBytes() == 0 {
					p.Confirm = false
					p.Message = "The source archive has insufficient capacity. Ask the operator to review its configured limits. No history was removed."
				}
			}
		}
		if r.Context().Err() != nil {
			http.Error(w, "Session or request expired", 403)
			return
		}
		if p.Confirm {
			p.Proof = s.proof(csrf, name, kind, id, v.Baseline, time.Now().Add(5*time.Minute).Unix())
		}
		h.renderPartial(w, r, "stack-history", p)
		return
	}
	if !p.Confirm || !s.verifyProof(r.PostForm.Get("review"), csrf, name, kind, id, v.Baseline) {
		http.Error(w, "Review expired or source changed. Open History and compare again. No revision was written.", 409)
		return
	}
	actor := auth.Identity(r)
	err = s.workspace.CommitSource(r.Context(), name, v.Baseline, func(current store.SourceView) (store.SourceUpdate, error) {
		if err := h.agent.Dill().CheckSourceProtection(name); err != nil {
			return store.SourceUpdate{}, store.ErrSourceUnavailable
		}
		return s.createUpdate(r.Context(), current, actor, id)
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSourceStale):
			http.Error(w, "Source changed after review. Nothing restored.", 409)
		case errors.Is(err, snapshothistory.ErrCapacity):
			http.Error(w, "The source archive has insufficient capacity. No source was changed or history removed.", 409)
		case errors.Is(err, store.ErrSourceInterrupted), errors.Is(err, store.ErrSourceUncertain):
			http.Error(w, "Publication unconfirmed. Preserve workspace and archive; inspect before any further write. Do not repeat this request.", 409)
		default:
			http.Error(w, "Source revision could not be confirmed. Inspect history and archive capacity before another change.", 409)
		}
		return
	}
	destination := "/stacks/" + name + "?tab=history"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
func (s *sourceHistoryAccess) checkpointHead(v store.SourceView) (parent string, changed bool, err error) {
	n := len(v.Revisions)
	if n == 0 {
		return "", true, nil
	}
	head, err := s.record(v, n-1)
	if err != nil {
		return "", false, err
	}
	return v.Revisions[n-1], head.Entrypoint != v.Entrypoint || !maps.EqualFunc(head.Files, v.Files, bytes.Equal), nil
}

func (s *sourceHistoryAccess) createUpdate(ctx context.Context, v store.SourceView, actor, id string) (store.SourceUpdate, error) {
	var update store.SourceUpdate
	parent, retain, err := s.checkpointHead(v)
	if err != nil {
		return update, err
	}
	var target snapshothistory.SourceRevision
	needed := 0
	if retain {
		needed++
	}
	if id != "" {
		var err error
		target, err = s.record(v, slices.Index(v.Revisions, id))
		if err != nil {
			return update, err
		}
		needed++
	}
	if needed == 0 {
		return update, store.ErrSourceUnavailable
	}
	records := make([]snapshothistory.SourceRevision, 0, needed)
	add := func(entry, from string, files map[string][]byte) {
		records = append(records, snapshothistory.SourceRevision{Actor: actor, Created: time.Now().UTC(), Parent: parent, RestoredFrom: from, Entrypoint: entry, Files: files})
	}
	if retain {
		add(v.Entrypoint, "", v.Files)
	}
	if id != "" {
		add(target.Entrypoint, id, target.Files)
		update.Files = target.Files
		update.Entrypoint = target.Entrypoint
	}
	ids, err := s.vault.PutChain(ctx, v.Scope, v.Revisions, records)
	if err != nil {
		return store.SourceUpdate{}, err
	}
	update.NewRevisions = ids
	return update, nil
}
