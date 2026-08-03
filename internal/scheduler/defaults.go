package scheduler

import "fmt"

// Family groups task types that may share a fallback concurrency limit.
const (
	FamilyIngest          = "ingest"
	FamilyPostIngest      = "post_ingest"
	FamilyPreview         = "preview"
	FamilySubtitle        = "subtitle"
	FamilyKeyframe        = "keyframe"
	FamilyScrape          = "scrape"
	FamilyPrepare         = "prepare"
	FamilyDocConvert      = "doc_convert"
	FamilyAI              = "ai"
	FamilyEncryption      = "encryption"
	FamilyRetirement      = "retirement"
	FamilyScan            = "scan"
	FamilyLyricRecognize  = "lyric_recognize"
	FamilyAudioAnalysis   = "audio_analysis"
	FamilyPhotoClassify   = "photo_classify"
	FamilyPhotoGeocode    = "photo_geocode"
	FamilyPhotoFace       = "photo_face"
	FamilyImageOCR        = "image_ocr"
	FamilyDocumentConvert = "document_convert"
	FamilyDocumentFulltext = "document_fulltext"
	FamilyPersonScrape    = "person_scrape"
	FamilyArtworkCover    = "artwork_cover"
)

// CompiledDefaults holds the default concurrency and resource profile for a registered task type.
type CompiledDefaults struct {
	TaskType           string
	Family             string
	DefaultConcurrency int
	Resources          ResourceRequest
	Provider           string
	ProfileVersion     int
}

// compiledDefaults is the canonical set of Phase 1-5 scheduler defaults.
var compiledDefaults = []CompiledDefaults{
	{TaskType: "ingest", Family: FamilyIngest, DefaultConcurrency: 3, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
	{TaskType: "metadata", Family: FamilyIngest, DefaultConcurrency: 3, Resources: ResourceRequest{CPU: 1, DiskRead: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "scan", Family: FamilyScan, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1}, ProfileVersion: 1},
	{TaskType: "scrape", Family: FamilyScrape, DefaultConcurrency: 6, Resources: ResourceRequest{CPU: 1, Network: 1}, ProfileVersion: 1},
	{TaskType: "poster", Family: FamilyPostIngest, DefaultConcurrency: 3, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "poster_repair", Family: FamilyPostIngest, DefaultConcurrency: 3, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "thumbnail", Family: FamilyPostIngest, DefaultConcurrency: 3, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "preview", Family: FamilyPreview, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "subtitle", Family: FamilySubtitle, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "subtitle_recognize", Family: FamilySubtitle, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
	{TaskType: "atrack", Family: FamilySubtitle, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "keyframe", Family: FamilyKeyframe, DefaultConcurrency: 3, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "encrypt", Family: FamilyEncryption, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
	{TaskType: "prepare", Family: FamilyPrepare, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "ai_analysis", Family: FamilyAI, DefaultConcurrency: 3, Resources: ResourceRequest{CPU: 1, Network: 1}, ProfileVersion: 1},
	{TaskType: "retirement", Family: FamilyRetirement, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
	// Phase 5 audio
	{TaskType: "lyric_recognize", Family: FamilyLyricRecognize, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
	{TaskType: "audio_analysis", Family: FamilyAudioAnalysis, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
	// Phase 5 image
	{TaskType: "photo_classify", Family: FamilyPhotoClassify, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1}, ProfileVersion: 1},
	{TaskType: "photo_geocode", Family: FamilyPhotoGeocode, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, Network: 1}, ProfileVersion: 1},
	{TaskType: "photo_face", Family: FamilyPhotoFace, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
	{TaskType: "image_ocr", Family: FamilyImageOCR, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
	// Phase 5 document
	{TaskType: "document_convert", Family: FamilyDocumentConvert, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "document_fulltext", Family: FamilyDocumentFulltext, DefaultConcurrency: 1, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	// Phase 5 other
	{TaskType: "person_scrape", Family: FamilyPersonScrape, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, Network: 1}, ProfileVersion: 1},
	{TaskType: "artwork_cover", Family: FamilyArtworkCover, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	// Legacy compatibility aliases (not Phase 5 canon)
	{TaskType: "doc_convert", Family: FamilyDocConvert, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}, ProfileVersion: 1},
	{TaskType: "lyric", Family: FamilyLyricRecognize, DefaultConcurrency: 2, Resources: ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}, ProfileVersion: 1},
}

// Registry maps task type names to their compiled defaults descriptor.
// Populated at init time; fails closed on duplicate or invalid entries.
var Registry map[string]Descriptor

func init() {
	Registry = make(map[string]Descriptor, len(compiledDefaults))
	for _, cd := range compiledDefaults {
		if cd.TaskType == "" {
			panic("scheduler: compiled default has empty task type")
		}
		if cd.Family == "" {
			panic(fmt.Sprintf("scheduler: compiled default %q has empty family", cd.TaskType))
		}
		if cd.DefaultConcurrency < 0 {
			panic(fmt.Sprintf("scheduler: compiled default %q has negative concurrency %d", cd.TaskType, cd.DefaultConcurrency))
		}
		if cd.ProfileVersion < 1 {
			panic(fmt.Sprintf("scheduler: compiled default %q has invalid profile version %d", cd.TaskType, cd.ProfileVersion))
		}
		for rk, count := range cd.Resources {
			if _, ok := AllResourceKinds[rk]; !ok {
				panic(fmt.Sprintf("scheduler: compiled default %q has unknown resource kind %q", cd.TaskType, rk))
			}
			if count < 0 {
				panic(fmt.Sprintf("scheduler: compiled default %q has negative resource request for %q", cd.TaskType, rk))
			}
		}
		if _, dup := Registry[cd.TaskType]; dup {
			panic(fmt.Sprintf("scheduler: duplicate compiled default for %q", cd.TaskType))
		}
		Registry[cd.TaskType] = Descriptor{
			TaskType:       cd.TaskType,
			Family:         cd.Family,
			ProfileVersion: cd.ProfileVersion,
			Resources:      cd.Resources,
			Provider:       cd.Provider,
		}
	}
}

// DefaultConcurrency returns the compiled default concurrency for a task type.
// Returns 0 and false if the type is not registered.
func DefaultConcurrency(taskType string) (int, bool) {
	for _, cd := range compiledDefaults {
		if cd.TaskType == taskType {
			return cd.DefaultConcurrency, true
		}
	}
	return 0, false
}

// FamilyDefaultConcurrency returns the compiled default concurrency for a family.
// Returns 0 and false if the family has no members.
func FamilyDefaultConcurrency(family string) (int, bool) {
	for _, cd := range compiledDefaults {
		if cd.Family == family {
			return cd.DefaultConcurrency, true
		}
	}
	return 0, false
}

// ValidateResourceRequest checks that every key in req is a known ResourceKind
// and every value is nonnegative. Returns an error describing the first violation.
func ValidateResourceRequest(req ResourceRequest) error {
	for rk, count := range req {
		if _, ok := AllResourceKinds[rk]; !ok {
			return fmt.Errorf("unknown resource kind %q", rk)
		}
		if count < 0 {
			return fmt.Errorf("negative resource request %d for %q", count, rk)
		}
	}
	return nil
}
