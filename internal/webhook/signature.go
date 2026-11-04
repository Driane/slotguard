// Package webhook delivers outbox events to an HTTP endpoint. Every request
// is signed, so the receiver can check that it comes from slotguard, that it
// was not altered and that it is not an old request replayed by an attacker.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Headers sent with every webhook.
const (
	SignatureHeader = "Slotguard-Signature"
	EventIDHeader   = "Slotguard-Event-Id"
)

// Errors returned by Verify.
var (
	ErrMalformedSignature = errors.New("missing or malformed signature header")
	ErrBadSignature       = errors.New("signature does not match")
	ErrStaleSignature     = errors.New("signature timestamp outside the tolerance")
)

// Sign returns the signature header for body sent at t, in the form
// "t=<unix seconds>,v1=<hex HMAC-SHA256 of "<unix seconds>.<body>">".
// Signing the timestamp with the body stops anyone from reusing a valid
// signature with another timestamp.
func Sign(secret []byte, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac(secret, ts, body))
}

// Verify checks a header produced by Sign. tolerance bounds the age of the
// timestamp, so a captured request cannot be replayed later.
func Verify(secret []byte, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			ts = value
		case "v1":
			sig = value
		}
	}

	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrMalformedSignature
	}
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) == 0 {
		return ErrMalformedSignature
	}
	// hmac.Equal takes the same time whatever the input, so an attacker
	// cannot guess the signature byte by byte from response times.
	if !hmac.Equal(got, mac(secret, ts, body)) {
		return ErrBadSignature
	}
	if age := now.Sub(time.Unix(unix, 0)); age > tolerance || age < -tolerance {
		return ErrStaleSignature
	}
	return nil
}

func mac(secret []byte, ts string, body []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(ts))
	h.Write([]byte("."))
	h.Write(body)
	return h.Sum(nil)
}
