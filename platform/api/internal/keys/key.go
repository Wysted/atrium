package keys

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

// NotifyChannel is the PostgreSQL channel notified whenever signing_keys changes.
const NotifyChannel = "signing_keys_changed"

var (
	// ErrNoActiveKey means there is no key able to sign.
	ErrNoActiveKey = errors.New("no active signing key")
	// ErrKeyNotFound means the kid does not match a published (non-removed) key.
	ErrKeyNotFound = errors.New("signing key not found")
	// ErrKEKMismatch means this replica's KEK is not the one that sealed the active or next keys. The
	// rotator refuses to act: it cannot tell corrupt keys from a misconfigured replica.
	ErrKEKMismatch = errors.New("KEK mismatch")
)

// KEKMismatchError is the typed form of ErrKEKMismatch. It carries fingerprints and kids only, never key material.
type KEKMismatchError struct {
	ReplicaKEKID string   // fingerprint of the KEK loaded by this replica
	KIDs         []string // active/next keys sealed under a different KEK
}

func (e *KEKMismatchError) Error() string {
	return fmt.Sprintf("%v: replica KEK %s does not match %d active/next key(s) %v", ErrKEKMismatch, e.ReplicaKEKID, len(e.KIDs), e.KIDs)
}

// Is makes errors.Is(err, ErrKEKMismatch) true.
func (e *KEKMismatchError) Is(target error) bool { return target == ErrKEKMismatch }

// checkKEK returns a *KEKMismatchError if any active or next key was sealed under another KEK. Retired
// keys do not sign and are never promoted, so they are not part of the check.
func checkKEK(kek *KEK, recs []Record) error {
	var bad []string
	for _, r := range recs {
		if (r.State == StateActive || r.State == StateNext) && r.KEKID != kek.ID() {
			bad = append(bad, r.KID)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return &KEKMismatchError{ReplicaKEKID: kek.ID(), KIDs: bad}
}

// State is a stage of the key lifecycle: next -> active -> retired -> removed.
type State string

const (
	StateNext    State = "next"    // published, does not sign
	StateActive  State = "active"  // signs
	StateRetired State = "retired" // still published, does not sign
	StateRemoved State = "removed" // not published; row kept, private material deleted
)

// published reports whether keys in this state appear in the JWKS.
func (s State) published() bool {
	return s == StateNext || s == StateActive || s == StateRetired
}

// Record is one signing_keys row.
type Record struct {
	KID         string
	State       State
	JWK         JWK
	PrivateEnc  []byte // nil once removed
	KEKID       string // fingerprint of the KEK that sealed PrivateEnc
	CreatedAt   time.Time
	ActivatedAt *time.Time
	RetiredAt   *time.Time
	RemovedAt   *time.Time
}

// PublicKey is the published half of a key.
type PublicKey struct {
	KID   string
	State State
	JWK   JWK
}

// Signer is an unsealed active key.
type Signer struct {
	KID string
	Key *ecdsa.PrivateKey
}

// Tx holds the mutations allowed while the rotation lock is held.
type Tx interface {
	// Now returns the current time of the database server. Every rotation decision and timestamp
	// uses it, never a replica clock, so skewed replicas cannot shorten or stretch a lifecycle.
	Now(ctx context.Context) (time.Time, error)
	// List returns every non-removed key ordered by creation time.
	List(ctx context.Context) ([]Record, error)
	Insert(ctx context.Context, r Record) error
	// SetState moves a key to a new state, stamping the matching timestamp with at.
	// Moving to StateRemoved also deletes the private material.
	SetState(ctx context.Context, kid string, to State, at time.Time) error
}

// Store is the persistence port, implemented by the postgres package.
type Store interface {
	// Locked runs fn in a transaction holding the key-rotation advisory lock. With wait=false and the
	// lock taken elsewhere it returns acquired=false without calling fn.
	Locked(ctx context.Context, wait bool, fn func(Tx) error) (acquired bool, err error)
	// LoadAll returns every non-removed key.
	LoadAll(ctx context.Context) ([]Record, error)
	// Subscribe calls notify once after listening starts and on every signing_keys change.
	// It blocks until ctx ends or the connection fails.
	Subscribe(ctx context.Context, notify func()) error
}

// newRecord generates a key in the given state, sealing its private half under kek.
func newRecord(kek *KEK, state State, now time.Time) (Record, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Record{}, fmt.Errorf("generate key: %w", err)
	}
	jwk, err := NewJWK(&priv.PublicKey)
	if err != nil {
		return Record{}, err
	}
	scalar, err := priv.Bytes()
	if err != nil {
		return Record{}, fmt.Errorf("encode private key: %w", err)
	}
	defer clear(scalar)
	sealed, err := kek.Seal(jwk.Kid, scalar)
	if err != nil {
		return Record{}, err
	}
	rec := Record{KID: jwk.Kid, State: state, JWK: jwk, PrivateEnc: sealed, KEKID: kek.ID(), CreatedAt: now}
	if state == StateActive {
		rec.ActivatedAt = &now
	}
	return rec, nil
}

// unseal recovers the signer of rec and checks that the key matches its kid.
func unseal(kek *KEK, rec Record) (Signer, error) {
	scalar, err := kek.Open(rec.KID, rec.PrivateEnc)
	if err != nil {
		return Signer{}, fmt.Errorf("key %s: %w", rec.KID, err)
	}
	defer clear(scalar)
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar)
	if err != nil {
		return Signer{}, fmt.Errorf("key %s: parse private key: %w", rec.KID, err)
	}
	kid, err := Thumbprint(&priv.PublicKey)
	if err != nil {
		return Signer{}, err
	}
	if kid != rec.KID {
		return Signer{}, fmt.Errorf("key %s: private key does not match kid", rec.KID)
	}
	return Signer{KID: rec.KID, Key: priv}, nil
}
