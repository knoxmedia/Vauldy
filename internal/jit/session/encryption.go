package session

// StreamEncryption configures play-time AES-128 HLS segment encryption for a JIT session.
type StreamEncryption struct {
	// Mode is standard | powerdrm | drm (drm uses powerdrm-style playlist tags for JIT TS).
	Mode string
	KidHex, KeyHex string
	// KeyInfoPath is the ffmpeg -hls_key_info_file path (URI line, key file, IV).
	KeyInfoPath string
}
