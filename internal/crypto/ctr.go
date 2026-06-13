package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

const ctrEncryptChunk = 256 * 1024

// ctrIV builds a 16-byte AES-CTR IV: nonce(12) || big-endian block counter(4).
func ctrIV(nonce [IVSize]byte, blockNum uint32) []byte {
	iv := make([]byte, aes.BlockSize)
	copy(iv, nonce[:])
	binary.BigEndian.PutUint32(iv[12:], blockNum)
	return iv
}

func encryptCTR(src io.Reader, dst io.Writer, block cipher.Block, nonce [IVSize]byte) error {
	stream := cipher.NewCTR(block, ctrIV(nonce, 0))
	buf := make([]byte, ctrEncryptChunk)
	out := make([]byte, ctrEncryptChunk)
	for {
		n, err := io.ReadFull(src, buf)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
		if n == 0 {
			break
		}
		stream.XORKeyStream(out[:n], buf[:n])
		if _, werr := dst.Write(out[:n]); werr != nil {
			return werr
		}
		if err != nil {
			break
		}
	}
	return nil
}

// ctrReadSeeker decrypts AES-CTR ciphertext on the fly with random access.
type ctrReadSeeker struct {
	file   *os.File
	block  cipher.Block
	nonce  [IVSize]byte
	size   int64
	pos    int64
	closed bool
}

func newCTRReadSeeker(f *os.File, block cipher.Block, nonce [IVSize]byte, plainSize int64) *ctrReadSeeker {
	return &ctrReadSeeker{file: f, block: block, nonce: nonce, size: plainSize}
}

func xorCTRAt(block cipher.Block, nonce [IVSize]byte, plainOffset int64, enc []byte) {
	blockNum := uint32(plainOffset / aes.BlockSize)
	skip := int(plainOffset % aes.BlockSize)
	stream := cipher.NewCTR(block, ctrIV(nonce, blockNum))
	if skip > 0 {
		pad := make([]byte, skip)
		stream.XORKeyStream(pad, pad)
	}
	stream.XORKeyStream(enc, enc)
}

func (c *ctrReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if c.closed {
		return 0, os.ErrClosed
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = c.pos + offset
	case io.SeekEnd:
		abs = c.size + offset
	default:
		return 0, errors.New("enc: invalid seek whence")
	}
	if abs < 0 {
		return 0, errors.New("enc: negative seek position")
	}
	if abs > c.size {
		abs = c.size
	}
	c.pos = abs
	return abs, nil
}

func (c *ctrReadSeeker) Read(p []byte) (int, error) {
	if c.closed {
		return 0, os.ErrClosed
	}
	if c.pos >= c.size {
		return 0, io.EOF
	}
	toRead := int64(len(p))
	if toRead > c.size-c.pos {
		toRead = c.size - c.pos
	}
	encOff := int64(EncHeaderSize) + c.pos
	if _, err := c.file.Seek(encOff, io.SeekStart); err != nil {
		return 0, err
	}
	encBuf := make([]byte, toRead)
	nEnc, err := io.ReadFull(c.file, encBuf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		if nEnc == 0 {
			return 0, err
		}
	}
	if nEnc == 0 {
		return 0, io.EOF
	}
	encBuf = encBuf[:nEnc]
	xorCTRAt(c.block, c.nonce, c.pos, encBuf)
	n := copy(p, encBuf)
	c.pos += int64(n)
	return n, nil
}

func (c *ctrReadSeeker) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.file.Close()
}

// ctrMemReadSeeker decrypts CTR ciphertext held in memory (tests / small payloads).
type ctrMemReadSeeker struct {
	enc    []byte
	block  cipher.Block
	nonce  [IVSize]byte
	size   int64
	pos    int64
	closed bool
}

func newCTRMemReadSeeker(enc []byte, hdr FileHeader, block cipher.Block, plainSize int64) *ctrMemReadSeeker {
	return &ctrMemReadSeeker{
		enc:   enc,
		block: block,
		nonce: hdr.Nonce,
		size:  plainSize,
	}
}

func (c *ctrMemReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if c.closed {
		return 0, os.ErrClosed
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = c.pos + offset
	case io.SeekEnd:
		abs = c.size + offset
	default:
		return 0, errors.New("enc: invalid seek whence")
	}
	if abs < 0 {
		return 0, errors.New("enc: negative seek position")
	}
	if abs > c.size {
		abs = c.size
	}
	c.pos = abs
	return abs, nil
}

func (c *ctrMemReadSeeker) Read(p []byte) (int, error) {
	if c.closed {
		return 0, os.ErrClosed
	}
	if c.pos >= c.size {
		return 0, io.EOF
	}
	toRead := int64(len(p))
	if toRead > c.size-c.pos {
		toRead = c.size - c.pos
	}
	encOff := int(EncHeaderSize) + int(c.pos)
	end := encOff + int(toRead)
	if end > len(c.enc) {
		end = len(c.enc)
	}
	if encOff >= end {
		return 0, io.EOF
	}
	encBuf := append([]byte(nil), c.enc[encOff:end]...)
	xorCTRAt(c.block, c.nonce, c.pos, encBuf)
	n := copy(p, encBuf)
	c.pos += int64(n)
	return n, nil
}

func (c *ctrMemReadSeeker) Close() error {
	c.closed = true
	c.enc = nil
	return nil
}
