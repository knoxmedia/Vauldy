package drm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestVerifySignedLicenseAcceptsValidToken(t *testing.T) {
	token, _, sig, kidVersion, sigVersion, err := BuildSignedLicense(
		"widevine",
		1,
		"kid-1",
		"ref-1",
		"nonce-1",
		"secret",
		"v1",
		"hmac-sha256-v1",
		2*time.Minute,
	)
	if err != nil {
		t.Fatalf("build license: %v", err)
	}
	claims, err := VerifySignedLicense(token, sig, "secret", kidVersion, sigVersion, time.Now().Unix())
	if err != nil {
		t.Fatalf("verify license: %v", err)
	}
	if claims.MediaID != 1 || claims.KID != "kid-1" || claims.DRMType != "widevine" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestVerifySignedLicenseRejectsTamperedSignature(t *testing.T) {
	token, _, _, kidVersion, sigVersion, err := BuildSignedLicense(
		"widevine",
		1,
		"kid-1",
		"ref-1",
		"nonce-1",
		"secret",
		"v1",
		"hmac-sha256-v1",
		2*time.Minute,
	)
	if err != nil {
		t.Fatalf("build license: %v", err)
	}
	if _, err := VerifySignedLicense(token, "bad-signature", "secret", kidVersion, sigVersion, time.Now().Unix()); err == nil {
		t.Fatalf("expected signature verification error")
	}
}

func TestVerifySignedLicenseRejectsExpiredToken(t *testing.T) {
	token, exp, sig, kidVersion, sigVersion, err := BuildSignedLicense(
		"widevine",
		1,
		"kid-1",
		"ref-1",
		"nonce-1",
		"secret",
		"v1",
		"hmac-sha256-v1",
		time.Second,
	)
	if err != nil {
		t.Fatalf("build license: %v", err)
	}
	if _, err := VerifySignedLicense(token, sig, "secret", kidVersion, sigVersion, exp+10); err == nil {
		t.Fatalf("expected expiry verification error")
	}
}

func TestVerifySignedLicenseRejectsMissingRequiredClaims(t *testing.T) {
	exp := time.Now().Add(2 * time.Minute).Unix()
	claims := map[string]any{
		"drm_type":    "",
		"media_id":    1,
		"kid":         "kid-1",
		"kid_version": "v1",
		"key_ref":     "ref-1",
		"nonce":       "n",
		"iat":         time.Now().Unix(),
		"exp":         exp,
		"sig_version": "hmac-sha256-v1",
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	token := base64.StdEncoding.EncodeToString(raw)
	canonical := BuildCanonicalString("", 1, "kid-1", "ref-1", exp, "n", "v1", "hmac-sha256-v1")
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(canonical))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if _, err := VerifySignedLicense(token, sig, "secret", "v1", "hmac-sha256-v1", time.Now().Unix()); err == nil {
		t.Fatalf("expected invalid payload error for missing drm_type")
	}
}
