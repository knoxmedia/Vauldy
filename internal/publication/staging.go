package publication

// ArtifactKind identifies a publication prerequisite artifact.
type ArtifactKind string

const (
	ArtifactPoster    ArtifactKind = "poster"
	ArtifactThumbnail ArtifactKind = "thumbnail"
	ArtifactEncrypt   ArtifactKind = "encrypt"
)

type StageRequest struct {
	MediaID, RunID, StepID, Generation        int64
	OwnerToken, SourcePath, SourceFingerprint string
}
type StageRecord struct {
	StageID                                         string
	Request                                         StageRequest
	Kind                                            ArtifactKind
	State, OriginalPath, QuarantinePath, StagedPath string
	HashesSizesJSON                                 string
}
