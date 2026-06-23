package storage

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"

	"database/sql"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
)

const isoBMFFLayoutScanLimit = 64 << 20 // 64 MiB from file start

// isoBMFFMoovBeforeMDAT reports whether moov appears before the first mdat in ISO-BMFF.
// This is the layout ffmpeg needs to demux Knox decrypt pipe:0 (no seek).
func isoBMFFMoovBeforeMDAT(r io.ReadSeeker) (bool, error) {
	if r == nil {
		return false, fmt.Errorf("nil reader")
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	var scanned int64
	for {
		if scanned >= isoBMFFLayoutScanLimit {
			return false, nil
		}
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return false, nil
			}
			return false, err
		}
		scanned += 8
		size32 := binary.BigEndian.Uint32(hdr[0:4])
		typ := string(hdr[4:8])

		switch typ {
		case "moov":
			return true, nil
		case "mdat":
			return false, nil
		}

		var boxSize int64
		switch size32 {
		case 0:
			// mdat with size 0 extends to EOF; without moov before it, layout is not pipe-safe.
			return false, nil
		case 1:
			var ext [8]byte
			if _, err := io.ReadFull(r, ext[:]); err != nil {
				return false, err
			}
			scanned += 8
			boxSize = int64(binary.BigEndian.Uint64(ext[:]))
			if boxSize < 16 {
				return false, fmt.Errorf("invalid extended box size")
			}
		default:
			boxSize = int64(size32)
			if boxSize < 8 {
				return false, fmt.Errorf("invalid box size %d", boxSize)
			}
		}
		payload := boxSize - 8
		if size32 == 1 {
			payload = boxSize - 16
		}
		if payload <= 0 {
			continue
		}
		if _, err := r.Seek(payload, io.SeekCurrent); err != nil {
			return false, err
		}
		scanned += payload
	}
}

func isoBMFFMoovBeforeMDATFile(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, fmt.Errorf("empty path")
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	return isoBMFFMoovBeforeMDAT(f)
}

func encryptedISOMoovBeforeMDAT(db *sql.DB, vault *keystore.Vault, mediaID int64, encPath string) bool {
	encPath = strings.TrimSpace(encPath)
	if !kcrypto.IsEncFile(encPath) || db == nil || vault == nil || mediaID <= 0 {
		return false
	}
	rc, err := OpenPlaintext(db, vault, mediaID, encPath)
	if err != nil {
		return false
	}
	defer func() { _ = rc.Close() }()
	rs, ok := rc.(io.ReadSeeker)
	if !ok {
		return false
	}
	okLayout, err := isoBMFFMoovBeforeMDAT(rs)
	return err == nil && okLayout
}
