package license

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// computeHMAC returns the lowercase hex HMAC-SHA256(secret, payload).
func computeHMAC(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// builtinSigningSecret is re-exported here so hmac.go has a stable reference
// without import cycles. The constant lives in module.go.
