package tradinglab

import (
	"encoding/base64"
	"errors"
	"strings"
)

// Sealed secrets name their scheme: "dpapi:" is Windows DPAPI for the
// worker's OS user (sealSecret on Windows); "plain:" relies on the data
// directory's permissions, as the worker's own credentials do elsewhere.
const (
	sealDPAPI = "dpapi:"
	sealPlain = "plain:"
)

func openSecret(sealed string) ([]byte, error) {
	switch {
	case strings.HasPrefix(sealed, sealPlain):
		return base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealPlain))
	case strings.HasPrefix(sealed, sealDPAPI):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealDPAPI))
		if err != nil {
			return nil, err
		}
		return unprotect(raw)
	}
	return nil, errors.New("a stored secret is unreadable")
}
