// Package brokerwire mirrors the unpublished Dill broker contract, not the
// released operation/preflight contracts. Transport bytes must never be logged.
package brokerwire

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

type Requirements struct {
	Contract     string   `json:"contract"`
	Stack        string   `json:"stack"`
	Engine       string   `json:"engine"`
	ConfigDigest string   `json:"configDigest"`
	Names        []string `json:"names"`
	Grants       []Grant  `json:"grants"`
}

type Review struct {
	Handle    string    `json:"handle"`
	Scope     Scope     `json:"scope"`
	ExpiresAt time.Time `json:"expiresAt"`
	Active    bool      `json:"active"`
}

func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type Values interface {
	Each(func(string, []byte) error) error
}

func FromValues(s Scope, values Values) (Envelope, error) {
	e := Envelope{Scope: s}
	if values == nil || !s.Valid() {
		return e, ErrInvalid
	}
	size := 0
	err := values.Each(func(name string, raw []byte) error {
		if len(e.Items) >= MaxEntries || len(raw) > MaxValue || size+len(raw) > MaxBundle {
			return ErrCapacity
		}
		item, err := NewItem(name, raw)
		if err != nil {
			return err
		}
		e.Items = append(e.Items, item)
		size += len(raw)
		return nil
	})
	if err != nil {
		e.Close()
		return Envelope{}, ErrUnavailable
	}
	return e, nil
}
