package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

// CanonicalJSON renders a JSON document in the one form its hash is taken
// over: object keys sorted, no insignificant whitespace, no HTML escaping,
// and every number exactly as written. It returns that form and its SHA-256
// in hex. Two documents that differ only in key order or spacing hash alike.
// A trailing second value is refused, so the hash covers the whole input.
func CanonicalJSON(doc string) (string, string, error) {
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", "", invalidf("the structured output is not valid JSON: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", "", invalidf("the structured output holds more than one JSON value")
	}
	canonical, err := encodeCanonical(v)
	if err != nil {
		return "", "", err
	}
	return canonical, HashHex(canonical), nil
}

// HashHex is the SHA-256 of s in lowercase hex.
func HashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// encodeCanonical marshals a decoded document. encoding/json already sorts
// map keys and keeps a json.Number's text; it only needs HTML escaping off.
func encodeCanonical(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// redactKeys removes every object key named in keys, at any depth.
func redactKeys(v any, keys map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if !keys[k] {
				out[k] = redactKeys(val, keys)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactKeys(val, keys)
		}
		return out
	default:
		return v
	}
}

// RedactJSON returns doc's canonical form without the named keys.
func RedactJSON(doc string, keys []string) (string, error) {
	if len(keys) == 0 {
		canonical, _, err := CanonicalJSON(doc)
		return canonical, err
	}
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	return encodeCanonical(redactKeys(v, drop))
}
