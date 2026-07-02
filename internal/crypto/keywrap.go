package crypto

import (
	"crypto/aes"
	"errors"
)

// AESKeyWrap implements RFC 3394 AES key wrap.
func AESKeyWrap(kek, plaintext []byte) ([]byte, error) {
	if len(plaintext)%8 != 0 {
		return nil, errors.New("keywrap: plaintext length must be multiple of 8")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(plaintext) / 8
	ciphertext := make([]byte, 8+n*8)
	iv := []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}
	copy(ciphertext, iv)
	copy(ciphertext[8:], plaintext)

	tmp := make([]byte, 16)
	for j := 0; j <= 5; j++ {
		for i := 1; i <= n; i++ {
			copy(tmp[:8], ciphertext[:8])
			copy(tmp[8:], ciphertext[i*8:(i+1)*8])
			block.Encrypt(tmp, tmp)
			t := uint64(n*j + i)
			for k := 7; k >= 0; k-- {
				tmp[k] ^= byte(t & 0xFF)
				t >>= 8
			}
			copy(ciphertext[:8], tmp[:8])
			copy(ciphertext[i*8:(i+1)*8], tmp[8:])
		}
	}
	return ciphertext, nil
}

// AESKeyUnwrap unwraps RFC 3394 wrapped key material.
func AESKeyUnwrap(kek, ciphertext []byte) ([]byte, error) {
	if len(ciphertext)%8 != 0 || len(ciphertext) < 16 {
		return nil, errors.New("keywrap: invalid ciphertext length")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := (len(ciphertext) / 8) - 1
	buf := make([]byte, len(ciphertext))
	copy(buf, ciphertext)

	tmp := make([]byte, 16)
	for j := 5; j >= 0; j-- {
		for i := n; i >= 1; i-- {
			t := uint64(n*j + i)
			copy(tmp[:8], buf[:8])
			for k := 7; k >= 0; k-- {
				tmp[k] ^= byte(t & 0xFF)
				t >>= 8
			}
			copy(tmp[8:], buf[i*8:(i+1)*8])
			block.Decrypt(tmp, tmp)
			copy(buf[:8], tmp[:8])
			copy(buf[i*8:(i+1)*8], tmp[8:])
		}
	}
	iv := []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}
	for i := 0; i < 8; i++ {
		if buf[i] != iv[i] {
			return nil, errors.New("keywrap: integrity check failed")
		}
	}
	return buf[8:], nil
}
