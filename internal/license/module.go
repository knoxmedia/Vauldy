// Package license implements the commercial feature-gating middleware for
// knox-media. It reads a license_key stored in system_options, verifies the
// HMAC-SHA256 signature, and exposes RequireFeature middleware that returns
// 403 when the feature is not enabled or the license has expired.
//
// License key format (base64url of JSON):
//
//	{"pretranscode":true,"trial":false,"exp":1735689600,"features":["pretranscode"]}
//	.<hex HMAC-SHA256(secret, json)>
//
// The secret is the knox-media signing key configured via system_options
// license_secret, defaulting to a build-embedded constant when unset.
package license

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/config"
	"knox-media/internal/coreiface"
)

// builtinSigningSecret is the fallback HMAC secret when no secret is configured
// in system_options. Operators override via the license_secret system option.
const builtinSigningSecret = "knox-media-license-signing-secret-v1"

// Module implements coreiface.EnterpriseModule for the license subsystem.
type Module struct {
	db *sql.DB
}

// NewModule returns the license enterprise module. The module itself only
// exposes middleware helpers; it does not register routes.
func NewModule() *Module { return &Module{} }

func (m *Module) Name() string { return "license" }

func (m *Module) Init(ctx context.Context, deps coreiface.ModuleDeps) error {
	m.db = deps.DB
	return nil
}

func (m *Module) RegisterRoutes(r coreiface.RouterGroup, deps coreiface.ModuleDeps) {
	// License status endpoint (read-only, admin-only via existing middleware).
	r.GET("/admin/license/status", m.handleStatus)
}

func (m *Module) RegisterWorkers(s coreiface.WorkerScheduler) {}

func (m *Module) Shutdown(ctx context.Context) error { return nil }

// Claims is the parsed license payload.
type Claims struct {
	Pretranscode bool     `json:"pretranscode"`
	Trial        bool     `json:"trial"`
	Exp          int64    `json:"exp"`
	Features     []string `json:"features"`
}

// Status describes the current license state for the frontend.
type Status struct {
	Valid           bool   `json:"valid"`
	Pretranscode    bool   `json:"pretranscode"`
	Trial           bool   `json:"trial"`
	ExpiresAt       int64  `json:"expires_at"`
	DaysRemaining   int    `json:"days_remaining"`
	ExpiringSoon    bool   `json:"expiring_soon"`
	Expired         bool   `json:"expired"`
	TrialUsedCount  int    `json:"trial_used_count"`
	TrialLimit      int    `json:"trial_limit"`
	TrialLimitMet   bool   `json:"trial_limit_met"`
	ErrorCode       string `json:"error_code,omitempty"`
}

// loadStatus reads the license key from system_options and verifies it.
// Returns a Status with Valid=false when no key is configured or the
// signature/expiry fails.
func (m *Module) loadStatus() Status {
	if m == nil || m.db == nil {
		return Status{ErrorCode: "no_db"}
	}
	var raw string
	_ = m.db.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw)
	key, secret := extractLicenseFields(raw)
	if key == "" {
		// Commercial builds without a license key get full access by default.
		if config.Edition == "commercial" {
			return Status{
				Valid:        true,
				Pretranscode: true,
			}
		}
		return Status{ErrorCode: "missing_key"}
	}
	if secret == "" {
		secret = builtinSigningSecret
	}
	claims, err := parseLicense(key, secret)
	if err != nil {
		return Status{ErrorCode: err.Error()}
	}
	now := time.Now().Unix()
	expired := claims.Exp > 0 && claims.Exp < now
	days := 0
	if claims.Exp > 0 {
		days = int((claims.Exp - now) / 86400)
		if days < 0 {
			days = 0
		}
	}
	trialUsed := 0
	if claims.Trial {
		_ = m.db.QueryRow(`SELECT COUNT(1) FROM pretranscode_rendition_job WHERE status='done'`).Scan(&trialUsed)
	}
	return Status{
		Valid:          !expired && claims.Pretranscode,
		Pretranscode:   claims.Pretranscode && !expired,
		Trial:          claims.Trial,
		ExpiresAt:      claims.Exp,
		DaysRemaining:  days,
		ExpiringSoon:   !expired && days > 0 && days <= 30,
		Expired:        expired,
		TrialUsedCount: trialUsed,
		TrialLimit:     10,
		TrialLimitMet:  claims.Trial && trialUsed >= 10,
	}
}

// RequireFeature returns gin middleware that rejects requests when the named
// feature is not enabled by a valid license. trial mode caps the number of
// completed pretranscode rendition jobs at 10 (SRS LIC-06).
func RequireFeature(feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// The license middleware uses its own singleton (assigned in init())
		// rather than the coreiface.PretranscodeMod handle, which is bound to
		// the pretranscode module, not the license module.
		mod := licenseSingleton
		if mod == nil || mod.db == nil {
			c.AbortWithStatusJSON(403, gin.H{"error": "license module not initialized"})
			return
		}
		st := mod.loadStatus()
		if st.Expired {
			c.AbortWithStatusJSON(403, gin.H{"error": "License Expired", "code": "license_expired"})
			return
		}
		if !st.Pretranscode {
			c.AbortWithStatusJSON(403, gin.H{"error": "Feature Not Licensed", "code": "feature_not_licensed"})
			return
		}
		if feature == "pretranscode" && st.TrialLimitMet {
			c.AbortWithStatusJSON(403, gin.H{"error": "Trial Limit Reached", "code": "trial_limit_reached"})
			return
		}
		if st.ExpiringSoon {
			c.Header("X-Knox-License-Warning", fmt.Sprintf("license expires in %d days", st.DaysRemaining))
		}
		c.Next()
	}
}

// licenseSingleton is assigned by NewModule() so the middleware factory can
// reach the active module without a request-time registry lookup.
var licenseSingleton *Module

func init() {
	// Register a module factory so coreiface has something to initialize.
	// The actual DB binding happens in Init().
	m := NewModule()
	licenseSingleton = m
	coreiface.RegisterEnterpriseModule(m)
}

// handleStatus returns the license status JSON for the admin UI.
func (m *Module) handleStatus(c *gin.Context) {
	c.JSON(200, m.loadStatus())
}

// SetSingletonForTest overrides the package-level singleton used by
// RequireFeature. Tests call this to bind a module with a test DB so the
// middleware can read the license state without running the full init().
func SetSingletonForTest(m *Module) {
	licenseSingleton = m
}

// NewModuleWithDB is a test helper that constructs a module bound to the
// given DB without running Init(). Production code uses NewModule + Init.
func NewModuleWithDB(db *sql.DB) *Module {
	m := &Module{db: db}
	return m
}

// parseLicense splits the key into payload.signature, verifies the HMAC, and
// decodes the JSON claims.
func parseLicense(key, secret string) (*Claims, error) {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed_key")
	}
	payloadB64, sigHex := parts[0], parts[1]
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, errors.New("payload_decode_error")
	}
	wantSig := computeHMAC(payload, secret)
	if !strings.EqualFold(wantSig, sigHex) {
		return nil, errors.New("bad_signature")
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("claims_decode: %w", err)
	}
	return &c, nil
}

// IssueLicense builds a signed license key for testing. Not exposed via HTTP;
// tests use it to mint fixtures without depending on an external issuer.
func IssueLicense(secret string, c Claims) string {
	if secret == "" {
		secret = builtinSigningSecret
	}
	payload, _ := json.Marshal(c)
	b64 := base64.RawURLEncoding.EncodeToString(payload)
	sig := computeHMAC(payload, secret)
	return b64 + "." + sig
}

// extractLicenseFields reads license_key and license_secret from the
// system_options JSON blob. Returns empty strings when absent.
func extractLicenseFields(optionsJSON string) (key, secret string) {
	if optionsJSON == "" {
		return "", ""
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(optionsJSON), &raw); err != nil {
		return "", ""
	}
	if v, ok := raw["license_key"].(string); ok {
		key = v
	}
	if v, ok := raw["license_secret"].(string); ok {
		secret = v
	}
	return key, secret
}
