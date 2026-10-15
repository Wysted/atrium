package keys_test

import (
	"bytes"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"platform/api/internal/keys"
)

func TestKEKIDIsStableDistinctAndOpaque(t *testing.T) {
	a, rawA, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	a2, err := keys.NewKEK(rawA)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := keys.GenerateKEK()

	if a.ID() != a2.ID() {
		t.Fatal("the same KEK must have a stable fingerprint")
	}
	if a.ID() == b.ID() {
		t.Fatal("different KEKs share a fingerprint")
	}
	if len(a.ID()) != 22 { // 16 bytes, base64url without padding
		t.Fatalf("fingerprint %q has length %d, want 22", a.ID(), len(a.ID()))
	}
	if strings.Contains(a.ID(), string(rawA)) || bytes.Contains([]byte(a.ID()), rawA) {
		t.Fatal("fingerprint leaks the KEK")
	}
}

// snapshotKeys captures every signing_keys column that a rotator could change.
func (r *rig) snapshotKeys(t *testing.T) []string {
	t.Helper()
	rows, err := r.pool.Query(r.ctx(t), `SELECT concat_ws('|', kid, state, kek_id, encode(coalesce(private_enc, ''), 'hex'),
		coalesce(activated_at::text, ''), coalesce(retired_at::text, ''), coalesce(removed_at::text, ''))
		FROM signing_keys ORDER BY kid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNewKeysStoreTheKEKFingerprint(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	var n int
	if err := r.pool.QueryRow(r.ctx(t), "SELECT count(*) FROM signing_keys WHERE kek_id = $1", r.kek.ID()).Scan(&n); err != nil || n != 2 {
		t.Fatalf("keys with the replica fingerprint = %d (err %v), want 2", n, err)
	}
	if err := r.pool.QueryRow(r.ctx(t), "SELECT count(*) FROM signing_keys WHERE kek_id <> $1", r.kek.ID()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("keys with another fingerprint = %d (err %v)", n, err)
	}
	if r.ring.KEKMismatch() {
		t.Fatal("matching KEK reported as mismatch")
	}
}

// A replica with the wrong KEK must not rotate, revoke or modify any row, even when a rotation is due.
func TestWrongKEKReplicaNeitherRotatesNorRevokesNorTouchesRows(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	active := r.activeKID(t)
	r.clock.Advance(keys.ActiveDuration + keys.RetiredDuration) // a healthy replica would swap now
	before := r.snapshotKeys(t)

	other, _, _ := keys.GenerateKEK()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	rotator, err := keys.NewRotator(keys.RotatorConfig{Store: r.store, KEK: other, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: r.store, KEK: other, Logger: log})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := rotator.Tick(r.ctx(t)); !errors.Is(err, keys.ErrKEKMismatch) {
		t.Fatalf("Tick error = %v, want ErrKEKMismatch", err)
	} else {
		var mm *keys.KEKMismatchError
		if !errors.As(err, &mm) || mm.ReplicaKEKID != other.ID() || len(mm.KIDs) != 2 {
			t.Fatalf("typed error = %+v", mm)
		}
	}
	if err := rotator.Revoke(r.ctx(t), active); !errors.Is(err, keys.ErrKEKMismatch) {
		t.Fatalf("Revoke error = %v, want ErrKEKMismatch", err)
	}
	if err := ring.Reload(r.ctx(t)); !errors.Is(err, keys.ErrKEKMismatch) {
		t.Fatalf("Reload error = %v, want ErrKEKMismatch", err)
	}
	if !ring.KEKMismatch() {
		t.Fatal("KEKMismatch not exposed")
	}
	if _, err := ring.Signer(); err == nil {
		t.Fatal("wrong-KEK replica can sign")
	}
	if len(ring.PublicKeys()) != 2 {
		t.Fatalf("published keys = %d, the public set must still be served", len(ring.PublicKeys()))
	}

	if after := r.snapshotKeys(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("rows changed under a wrong KEK:\nbefore %v\nafter  %v", before, after)
	}
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "wrong KEK") {
		t.Fatalf("no clear ERROR log:\n%s", out)
	}

	// The right replica recovers: the keyring clears the flag and the rotator acts.
	if err := r.ring.Reload(r.ctx(t)); err != nil || r.ring.KEKMismatch() {
		t.Fatalf("healthy replica: err=%v mismatch=%v", err, r.ring.KEKMismatch())
	}
	if res, err := r.rotator.Tick(r.ctx(t)); err != nil || len(res.Events) == 0 {
		t.Fatalf("healthy tick = %+v, %v", res, err)
	}
}

func TestMixedKEKFingerprintsCountAsMismatch(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	if _, err := r.pool.Exec(r.ctx(t), "UPDATE signing_keys SET kek_id = repeat('z', 22) WHERE state = 'next'"); err != nil {
		t.Fatal(err)
	}
	before := r.snapshotKeys(t)
	r.clock.Advance(keys.ActiveDuration)
	if _, err := r.rotator.Tick(r.ctx(t)); !errors.Is(err, keys.ErrKEKMismatch) {
		t.Fatalf("Tick error = %v", err)
	}
	if after := r.snapshotKeys(t); !reflect.DeepEqual(before, after) {
		t.Fatal("rows changed")
	}
}

// With the right KEK a next key that cannot be decrypted is corrupt: the tick removes it and creates a new one.
func TestCorruptNextWithMatchingKEKIsRemediated(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	active, broken := r.activeKID(t), r.nextKID(t)
	r.corrupt(t, broken)

	logs := &syncBuffer{}
	rotator, err := keys.NewRotator(keys.RotatorConfig{Store: r.store, KEK: r.kek, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.clock.Advance(time.Minute)
	res, err := rotator.Tick(r.ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(res); !reflect.DeepEqual(got, []keys.ActionKind{keys.ActionRemove, keys.ActionCreateNext}) {
		t.Fatalf("events = %v", got)
	}
	if got := r.stateOf(t, broken); got != "removed" {
		t.Fatalf("corrupt next state = %q", got)
	}
	if got := r.stateOf(t, active); got != "active" {
		t.Fatalf("active state = %q", got)
	}
	if n := countKeys(t, r, "next"); n != 1 {
		t.Fatalf("next keys = %d, want one fresh replacement", n)
	}
	if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, broken) {
		t.Fatalf("no ERROR naming the corrupt key:\n%s", out)
	}
	// The replacement is usable: it can be unsealed by the same KEK.
	if err := r.ring.Reload(r.ctx(t)); err != nil {
		t.Fatal(err)
	}
	if res, err := r.rotator.Tick(r.ctx(t)); err != nil || len(res.Events) != 0 {
		t.Fatalf("second tick = %+v, %v (want idempotent)", res, err)
	}
}
