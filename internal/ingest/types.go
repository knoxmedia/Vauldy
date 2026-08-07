package ingest

type Source string

const (
	SourceFilesystemEvent Source = "filesystem_event"
	SourceUpload          Source = "upload"
	SourceScan            Source = "scan"
)

// Candidate.PathKey is a best-effort index key for convergence only; it is never
// authorization evidence or proof of content/file identity.
type Candidate struct {
	Source              Source
	LibraryID           int64
	Path                string
	PathKey             string
	UploadID            string
	ExpectedFingerprint *Fingerprint
}

type Fingerprint struct {
	SHA256    string
	Size      int64
	ModTimeNS int64
}

type PlanResult struct {
	ItemID     int64
	MediaID    int64
	RunID      int64
	Generation int64
	Duplicate  bool
}
