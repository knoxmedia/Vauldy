package crypto

import (
	"encoding/binary"
	"io"
	"os"
)

const (
	ModeGCM = byte(0x00)
	ModeCTR = byte(0x01)
)

// FileHeader is the Knox 9527 .enc file prefix.
type FileHeader struct {
	Version byte
	Mode    byte
	Nonce   [IVSize]byte
}

func writeHeader(dst io.Writer, mode byte, nonce []byte) error {
	if _, err := dst.Write([]byte(Magic9527)); err != nil {
		return err
	}
	if err := binary.Write(dst, binary.LittleEndian, Version); err != nil {
		return err
	}
	if err := binary.Write(dst, binary.LittleEndian, mode); err != nil {
		return err
	}
	if _, err := dst.Write(make([]byte, 2)); err != nil {
		return err
	}
	_, err := dst.Write(nonce[:IVSize])
	return err
}

func readHeader(r io.Reader) (FileHeader, error) {
	var hdr FileHeader
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil {
		return hdr, err
	}
	if string(magic) != Magic9527 {
		return hdr, ErrBadMagic
	}
	ver := make([]byte, 1)
	if _, err := io.ReadFull(r, ver); err != nil {
		return hdr, err
	}
	if ver[0] != Version {
		return hdr, ErrBadVersion
	}
	mode := make([]byte, 1)
	if _, err := io.ReadFull(r, mode); err != nil {
		return hdr, err
	}
	hdr.Version = ver[0]
	hdr.Mode = mode[0]
	if _, err := io.ReadFull(r, make([]byte, 2)); err != nil {
		return hdr, err
	}
	if _, err := io.ReadFull(r, hdr.Nonce[:]); err != nil {
		return hdr, err
	}
	return hdr, nil
}

func readHeaderFile(path string) (FileHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileHeader{}, err
	}
	defer f.Close()
	return readHeader(f)
}
