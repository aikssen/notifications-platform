package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Signature headers let a client prove that a delivery really came from the
// platform, and that it is not a replay of an older one.
//
// OWASP A08 (software and data integrity failures): without this, any party
// that learns a client's webhook URL can post fabricated payment events to it.
const (
	HeaderSignature = "X-Signature"
	HeaderTimestamp = "X-Timestamp"
)

// Sign returns the value for X-Signature over the canonical string
//
//	<unix timestamp> "." <raw request body>
//
// The timestamp is inside the signed material on purpose. Signing the body
// alone produces a token that stays valid forever, so anyone who captures one
// request can replay it indefinitely; binding the timestamp lets the receiver
// reject anything outside a short window.
func Sign(secret string, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp.Unix(), 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify re-computes the signature and compares it in constant time. It lives
// here so the reference implementation ships with the platform: this is the
// code a client would write on their side.
func Verify(secret, signature string, timestamp time.Time, body []byte) bool {
	return hmac.Equal([]byte(signature), []byte(Sign(secret, timestamp, body)))
}
