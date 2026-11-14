//go:build windows

package tradinglab

import (
	"encoding/base64"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sealSecret encrypts with DPAPI, so only the worker's Windows user on this
// computer can read it back.
func sealSecret(secret []byte) (string, error) {
	if len(secret) == 0 {
		return sealPlain, nil
	}
	in := windows.DataBlob{Size: uint32(len(secret)), Data: &secret[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	return sealDPAPI + base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, out.Size)), nil
}

func unprotect(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
