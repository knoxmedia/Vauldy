package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"time"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/widevine/license", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		if len(body) == 0 {
			http.Error(w, "empty challenge", http.StatusBadRequest)
			return
		}

		// This is a transport mock only (not a valid WV license):
		// respond with deterministic binary bytes so gateway/client can
		// verify raw challenge/response path.
		sum := sha256.Sum256(body)
		out := append([]byte("MOCK_WV_LICENSE_V1|"), []byte(hex.EncodeToString(sum[:]))...)

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)

		log.Printf("mock_wv: challenge_bytes=%d response_bytes=%d media_id=%q kid=%q",
			len(body), len(out), r.Header.Get("X-Knox-Media-ID"), r.Header.Get("X-Knox-KID"))
	})

	srv := &http.Server{
		Addr:              "127.0.0.1:9001",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Println("widevine mock server listening on http://127.0.0.1:9001")
	log.Fatal(srv.ListenAndServe())
}

