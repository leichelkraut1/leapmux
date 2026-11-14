package tradinglab

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The connector passphrase signs Claude, ChatGPT or Codex in to the lab's
// read-only connector. Only its argon2id hash is stored. (Approving a live
// order needs no passphrase: only the person's own browser session may.)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	minPassLen   = 10
)

// ErrPassphrase is a missing or wrong connector passphrase.
var ErrPassphrase = errors.New("the passphrase is wrong")

func hashPassphrase(pass string) (string, error) {
	if len(pass) < minPassLen {
		return "", fmt.Errorf("the passphrase needs at least %d characters", minPassLen)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pass), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func checkPassphrase(stored, pass string) error {
	if stored == "" {
		return errors.New("no passphrase is set; set one in the lab settings first")
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return errors.New("the stored passphrase hash is unreadable")
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return errors.New("the stored passphrase hash is unreadable")
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[3])
	want, err2 := enc.DecodeString(parts[4])
	if err1 != nil || err2 != nil {
		return errors.New("the stored passphrase hash is unreadable")
	}
	got := argon2.IDKey([]byte(pass), salt, t, m, p, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPassphrase
	}
	return nil
}
