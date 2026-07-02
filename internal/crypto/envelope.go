package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	Magic9527       = "\x39\x35\x32\x37"
	Version         = byte(1)
	DEKSize         = 32
	IVSize          = 12
	GCMTagSize      = 16
	EncHeaderSize   = 20 // magic(4)+ver(1)+mode(1)+reserved(2)+iv(12)
	EncOverheadSize = EncHeaderSize + GCMTagSize
)

var (
	ErrBadMagic   = errors.New("enc: bad magic number")
	ErrBadVersion = errors.New("enc: unsupported version")
	ErrIntegrity  = errors.New("enc: GCM integrity check failed")
)

// EnvelopeResult holds paths and key material after encryption.
type EnvelopeResult struct {
	FilePath   string
	WrappedDEK []byte
	IV         []byte
}

// PlaintextSize returns decrypted payload length from an on-disk .enc file without decrypting.
func PlaintextSize(encPath string) (int64, error) {
	st, err := os.Stat(encPath)
	if err != nil {
		return 0, err
	}
	hdr, err := readHeaderFile(encPath)
	if err != nil {
		return 0, err
	}
	switch hdr.Mode {
	case ModeCTR:
		if st.Size() < int64(EncHeaderSize) {
			return 0, fmt.Errorf("enc: file too small (%d bytes)", st.Size())
		}
		return st.Size() - int64(EncHeaderSize), nil
	default:
		if st.Size() < int64(EncOverheadSize) {
			return 0, fmt.Errorf("enc: file too small (%d bytes)", st.Size())
		}
		return st.Size() - int64(EncOverheadSize), nil
	}
}

// IsEncFile reports whether path looks like a Knox 9527 encrypted asset.
func IsEncFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return false
	}
	return string(magic) == Magic9527
}

// EncryptFile encrypts plaintext using AES-CTR streaming (mode 0x01); ciphertext length equals plaintext length.
func EncryptFile(src io.Reader, dst io.Writer, kek []byte) (*EnvelopeResult, error) {
	dek := make([]byte, DEKSize)
	var nonce [IVSize]byte
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("generate DEK: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, fmt.Errorf("generate IV: %w", err)
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	if err := writeHeader(dst, ModeCTR, nonce[:]); err != nil {
		return nil, err
	}
	if err := encryptCTR(src, dst, block, nonce); err != nil {
		return nil, err
	}

	wrappedDEK, err := AESKeyWrap(kek, dek)
	if err != nil {
		return nil, err
	}
	subtle.ConstantTimeCopy(1, dek, make([]byte, DEKSize))

	return &EnvelopeResult{WrappedDEK: wrappedDEK, IV: append([]byte(nil), nonce[:]...)}, nil
}

// OpenDecryptSeeker opens an .enc file and returns a streaming seekable plaintext reader.
func OpenDecryptSeeker(encPath string, wrappedDEK, kek []byte) (io.ReadSeekCloser, error) {
	dek, err := AESKeyUnwrap(kek, wrappedDEK)
	if err != nil {
		return nil, err
	}
	defer subtle.ConstantTimeCopy(1, dek, make([]byte, len(dek)))

	f, err := os.Open(encPath)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	hdr, err := readHeader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	switch hdr.Mode {
	case ModeCTR:
		plainSize := st.Size() - int64(EncHeaderSize)
		if plainSize < 0 {
			_ = f.Close()
			return nil, fmt.Errorf("enc: invalid CTR payload size")
		}
		return newCTRReadSeeker(f, block, hdr.Nonce, plainSize), nil
	default:
		ciphertext, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		plaintext, err := aead.Open(nil, hdr.Nonce[:], ciphertext, nil)
		if err != nil {
			return nil, ErrIntegrity
		}
		return newZeroReader(plaintext), nil
	}
}

// DecryptStream returns a ReadCloser over decrypted plaintext (prefer OpenDecryptSeeker for files).
func DecryptStream(src io.Reader, wrappedDEK, kek []byte) (io.ReadCloser, error) {
	if f, ok := src.(*os.File); ok {
		path := f.Name()
		_ = f.Close()
		rsc, err := OpenDecryptSeeker(path, wrappedDEK, kek)
		if err != nil {
			return nil, err
		}
		return rsc, nil
	}
	raw, err := io.ReadAll(src)
	if err != nil {
		return nil, err
	}
	return decryptFromBytes(raw, wrappedDEK, kek)
}

func decryptFromBytes(raw []byte, wrappedDEK, kek []byte) (io.ReadCloser, error) {
	dek, err := AESKeyUnwrap(kek, wrappedDEK)
	if err != nil {
		return nil, err
	}
	defer subtle.ConstantTimeCopy(1, dek, make([]byte, len(dek)))

	hdr, err := readHeader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	switch hdr.Mode {
	case ModeCTR:
		plainSize := int64(len(raw)) - int64(EncHeaderSize)
		if plainSize < 0 {
			return nil, fmt.Errorf("enc: invalid CTR payload size")
		}
		return newCTRMemReadSeeker(raw, hdr, block, plainSize), nil
	default:
		ciphertext := raw[EncHeaderSize:]
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		plaintext, err := aead.Open(nil, hdr.Nonce[:], ciphertext, nil)
		if err != nil {
			return nil, ErrIntegrity
		}
		return newZeroReader(plaintext), nil
	}
}

// DecryptFile opens an on-disk .enc file and returns all decrypted bytes (loads into memory).
func DecryptFile(encPath string, wrappedDEK, kek []byte) ([]byte, error) {
	rc, err := OpenDecryptSeeker(encPath, wrappedDEK, kek)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// zeroReader is an in-memory ReadSeekCloser that wipes plaintext on Close (legacy GCM only).
type zeroReader struct {
	data []byte
	pos  int64
}

func newZeroReader(plaintext []byte) *zeroReader {
	return &zeroReader{data: plaintext}
}

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.pos >= int64(len(z.data)) {
		return 0, io.EOF
	}
	n := copy(p, z.data[z.pos:])
	z.pos += int64(n)
	return n, nil
}

func (z *zeroReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = z.pos + offset
	case io.SeekEnd:
		abs = int64(len(z.data)) + offset
	default:
		return 0, errors.New("enc: invalid seek whence")
	}
	if abs < 0 {
		return 0, errors.New("enc: negative seek position")
	}
	if abs > int64(len(z.data)) {
		abs = int64(len(z.data))
	}
	z.pos = abs
	return abs, nil
}

func (z *zeroReader) Close() error {
	subtle.ConstantTimeCopy(1, z.data, make([]byte, len(z.data)))
	z.data = nil
	z.pos = 0
	return nil
}
