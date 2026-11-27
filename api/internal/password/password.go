// Package password hashes and verifies passwords with argon2id, stored as PHC strings.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/davcs86/commsgun/api/internal/constants"
)

type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

func Default() Params {
	return Params{MemoryKiB: constants.Argon2MemoryKiB, Time: constants.Argon2Time, Threads: constants.Argon2Threads}
}

func Valid(pw string) bool {
	return len(pw) <= constants.PasswordMaxBytes && utf8.RuneCountInString(pw) >= constants.PasswordMinChars
}

var errMalformed = errors.New("password: malformed hash")

var b64 = base64.RawStdEncoding.Strict()

type Hasher struct {
	p     Params
	slot  chan struct{}
	dummy string
}

func NewHasher(p Params, concurrency int) *Hasher {
	return &Hasher{p: p, slot: make(chan struct{}, concurrency), dummy: format(p, random(constants.Argon2SaltBytes), random(constants.Argon2KeyBytes))}
}

func (h *Hasher) Hash(ctx context.Context, pw string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := random(constants.Argon2SaltBytes)
	return format(h.p, salt, derive(pw, salt, h.p, constants.Argon2KeyBytes)), nil
}

// Verify takes m, t and p from phc, not from the Hasher; each must be at least 1.
func (h *Hasher) Verify(ctx context.Context, pw, phc string) (bool, error) {
	p, salt, key, err := parse(phc)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	return subtle.ConstantTimeCompare(derive(pw, salt, p, len(key)), key) == 1, nil
}

func (h *Hasher) VerifyDummy(ctx context.Context, pw string) error {
	_, err := h.Verify(ctx, pw, h.dummy)
	return err
}

func (h *Hasher) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case h.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slot }

func parse(phc string) (Params, []byte, []byte, error) {
	f := strings.Split(phc, "$")
	if len(f) != 6 || f[0] != "" || f[1] != "argon2id" || f[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return Params{}, nil, nil, errMalformed
	}
	kv := strings.Split(f[3], ",")
	if len(kv) != 3 {
		return Params{}, nil, nil, errMalformed
	}
	m, err1 := field(kv[0], "m=", 32)
	t, err2 := field(kv[1], "t=", 32)
	p, err3 := field(kv[2], "p=", 8)
	salt, err4 := b64.DecodeString(f[4])
	key, err5 := b64.DecodeString(f[5])
	if errors.Join(err1, err2, err3, err4, err5) != nil ||
		len(salt) != constants.Argon2SaltBytes || len(key) != constants.Argon2KeyBytes {
		return Params{}, nil, nil, errMalformed
	}
	return Params{MemoryKiB: uint32(m), Time: uint32(t), Threads: uint8(p)}, salt, key, nil
}

func field(s, prefix string, bits int) (uint64, error) {
	v, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return 0, errMalformed
	}
	n, err := strconv.ParseUint(v, 10, bits)
	if err != nil || n < 1 {
		return 0, errMalformed
	}
	return n, nil
}

func derive(pw string, salt []byte, p Params, keyLen int) []byte {
	return argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, uint32(keyLen))
}

func format(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

func random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
