package scheduler

// ResourceKind identifies a measurable scheduling resource.
type ResourceKind string

const (
	CPU              ResourceKind = "cpu"
	GPU              ResourceKind = "gpu"
	DiskRead         ResourceKind = "disk_read"
	DiskWrite        ResourceKind = "disk_write"
	Network          ResourceKind = "network"
	ExternalProcess  ResourceKind = "external_process"
)

// AllResourceKinds enumerates every known ResourceKind for validation.
var AllResourceKinds = map[ResourceKind]struct{}{
	CPU:             {},
	GPU:             {},
	DiskRead:        {},
	DiskWrite:       {},
	Network:         {},
	ExternalProcess: {},
}

// ResourceRequest maps resource kinds to integer token counts.
type ResourceRequest map[ResourceKind]int

// Descriptor declares the immutable scheduling profile for a task type.
type Descriptor struct {
	TaskType       string
	Family         string
	ProfileVersion int
	Resources      ResourceRequest
	Provider       string
}
