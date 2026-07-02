package drm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Service struct {
	DB *sql.DB
}

type SignedLicenseClaims struct {
	DRMType    string `json:"drm_type"`
	MediaID    int64  `json:"media_id"`
	KID        string `json:"kid"`
	KIDVersion string `json:"kid_version"`
	KeyRef     string `json:"key_ref"`
	Nonce      string `json:"nonce"`
	IssuedAt   int64  `json:"iat"`
	ExpiresAt  int64  `json:"exp"`
	SigVersion string `json:"sig_version"`
}

const (
	DefaultKIDVersion = "v1"
	DefaultSigVersion = "hmac-sha256-v1"
)

func NewService(db *sql.DB) *Service {
	return &Service{DB: db}
}

func (s *Service) ValidateMedia(mediaID int64) error {
	if s == nil || s.DB == nil || mediaID <= 0 {
		return errors.New("invalid media")
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM media WHERE id = ?`, mediaID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) Audit(mediaID int64, drmType, result, reason, clientIP string) {
	if s == nil || s.DB == nil {
		return
	}
	_, _ = s.DB.Exec(
		`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip) VALUES (?, ?, ?, ?, ?)`,
		mediaID, drmType, result, reason, clientIP,
	)
}

func (s *Service) ValidateMediaDRMAsset(mediaID int64) error {
	if s == nil || s.DB == nil || mediaID <= 0 {
		return errors.New("invalid media")
	}
	var kid, keyRef, manifestPath string
	if err := s.DB.QueryRow(`SELECT COALESCE(kid,''), COALESCE(key_ref,''), COALESCE(manifest_path,'') FROM drm_asset WHERE media_id = ? LIMIT 1`, mediaID).Scan(&kid, &keyRef, &manifestPath); err != nil {
		return err
	}
	if strings.TrimSpace(kid) == "" || strings.TrimSpace(keyRef) == "" || strings.TrimSpace(manifestPath) == "" {
		return errors.New("drm asset not ready")
	}
	if _, err := os.Stat(strings.TrimSpace(keyRef)); err != nil {
		return errors.New("drm asset not ready")
	}
	if _, err := os.Stat(strings.TrimSpace(manifestPath)); err != nil {
		return errors.New("drm asset not ready")
	}
	return nil
}

func (s *Service) GetDRMAsset(mediaID int64) (kid string, keyRef string, err error) {
	if s == nil || s.DB == nil || mediaID <= 0 {
		return "", "", errors.New("invalid media")
	}
	var k, r string
	if err := s.DB.QueryRow(`SELECT COALESCE(kid,''), COALESCE(key_ref,'') FROM drm_asset WHERE media_id = ? LIMIT 1`, mediaID).Scan(&k, &r); err != nil {
		return "", "", err
	}
	k = strings.TrimSpace(k)
	r = strings.TrimSpace(r)
	if k == "" || r == "" {
		return "", "", errors.New("drm asset not ready")
	}
	return k, r, nil
}

func BuildSignedLicense(drmType string, mediaID int64, kid, keyRef, nonce, secret, kidVersion, sigVersion string, ttl time.Duration) (encoded string, exp int64, sig string, outKIDVersion string, outSigVersion string, err error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	now := time.Now().UTC()
	exp = now.Add(ttl).Unix()
	outKIDVersion = strings.TrimSpace(kidVersion)
	if outKIDVersion == "" {
		outKIDVersion = DefaultKIDVersion
	}
	outSigVersion = strings.TrimSpace(sigVersion)
	if outSigVersion == "" {
		outSigVersion = DefaultSigVersion
	}
	payload := map[string]any{
		"drm_type":    drmType,
		"media_id":    mediaID,
		"kid":         kid,
		"kid_version": outKIDVersion,
		"key_ref":     keyRef,
		"nonce":       nonce,
		"iat":         now.Unix(),
		"exp":         exp,
		"sig_version": outSigVersion,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", 0, "", "", "", err
	}
	canonical := BuildCanonicalString(drmType, mediaID, kid, keyRef, exp, nonce, outKIDVersion, outSigVersion)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	sig = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	encoded = base64.StdEncoding.EncodeToString(raw)
	return encoded, exp, sig, outKIDVersion, outSigVersion, nil
}

func BuildCanonicalString(drmType string, mediaID int64, kid, keyRef string, exp int64, nonce, kidVersion, sigVersion string) string {
	return fmt.Sprintf("%s|%d|%s|%s|%d|%s|%s|%s", drmType, mediaID, kid, keyRef, exp, nonce, kidVersion, sigVersion)
}

func VerifySignedLicense(encodedLicense, sig, secret, expectKIDVersion, expectSigVersion string, nowUnix int64) (*SignedLicenseClaims, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedLicense))
	if err != nil {
		return nil, errors.New("invalid encoded license")
	}
	var claims SignedLicenseClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, errors.New("invalid license payload")
	}
	if strings.TrimSpace(claims.DRMType) == "" ||
		claims.MediaID <= 0 ||
		strings.TrimSpace(claims.KID) == "" ||
		strings.TrimSpace(claims.KeyRef) == "" ||
		strings.TrimSpace(claims.Nonce) == "" ||
		strings.TrimSpace(claims.KIDVersion) == "" ||
		strings.TrimSpace(claims.SigVersion) == "" ||
		claims.ExpiresAt <= 0 {
		return nil, errors.New("invalid license payload")
	}
	if nowUnix <= 0 {
		nowUnix = time.Now().UTC().Unix()
	}
	if claims.ExpiresAt > 0 && nowUnix > claims.ExpiresAt {
		return nil, errors.New("license expired")
	}
	if expectKIDVersion != "" && claims.KIDVersion != expectKIDVersion {
		return nil, errors.New("kid version mismatch")
	}
	if expectSigVersion != "" && claims.SigVersion != expectSigVersion {
		return nil, errors.New("sig version mismatch")
	}
	canonical := BuildCanonicalString(claims.DRMType, claims.MediaID, claims.KID, claims.KeyRef, claims.ExpiresAt, claims.Nonce, claims.KIDVersion, claims.SigVersion)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	expectedSig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expectedSig), []byte(sig)) {
		return nil, errors.New("invalid signature")
	}
	return &claims, nil
}
