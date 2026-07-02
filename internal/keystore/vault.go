package keystore

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Vault derives and holds the KEK in memory.
type Vault struct {
	mu      sync.RWMutex
	kek     []byte
	version int
	salt    []byte
}

// NewVault builds a KEK from mainKey and optional saltPath (created if missing).
func NewVault(mainKey, saltPath string) (*Vault, error) {
	mainKey = strings.TrimSpace(mainKey)
	if mainKey == "" {
		return nil, errors.New("keystore: main key not configured (set KNOX_MAIN_KEY or security.jwt_secret)")
	}
	salt, err := loadOrCreateSalt(saltPath)
	if err != nil {
		return nil, err
	}
	kek := argon2.IDKey([]byte(mainKey), salt, 3, 64*1024, 4, 32)
	return &Vault{kek: kek, version: 1, salt: salt}, nil
}

// GetKEK returns a copy of the active KEK.
func (v *Vault) GetKEK(ctx context.Context) ([]byte, error) {
	_ = ctx
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.kek == nil {
		return nil, errors.New("keystore: KEK not initialized")
	}
	out := make([]byte, len(v.kek))
	copy(out, v.kek)
	return out, nil
}

// Destroy zeroes the KEK in memory.
func (v *Vault) Destroy() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.kek != nil {
		subtle.ConstantTimeCopy(1, v.kek, make([]byte, len(v.kek)))
		v.kek = nil
	}
}

func loadOrCreateSalt(path string) ([]byte, error) {
	if path == "" {
		salt := make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		return salt, nil
	}
	if data, err := os.ReadFile(path); err == nil && len(data) >= 16 {
		return data, nil
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, salt, 0o600); err != nil {
		return nil, err
	}
	return salt, nil
}
