//go:build !windows

package tradinglab

import (
	"encoding/base64"
	"errors"
)

// sealSecret stores the secret as is: off Windows the worker's database
// relies on its data directory's permissions, like its other credentials.
func sealSecret(secret []byte) (string, error) {
	return sealPlain + base64.StdEncoding.EncodeToString(secret), nil
}

func unprotect([]byte) ([]byte, error) {
	return nil, errors.New("a secret sealed with Windows DPAPI cannot be read on this system")
}
