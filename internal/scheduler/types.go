package scheduler

import (
	"fmt"
	"strings"
)

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

// RevisionConflictError reports an optimistic-concurrency failure when the
// caller's expected policy revision no longer matches the active revision.
type RevisionConflictError struct {
	Expected int64
	Current  int64
}

func (e RevisionConflictError) Error() string {
	return fmt.Sprintf("policy revision conflict: expected=%d current=%d", e.Expected, e.Current)
}

// ControlConflictError reports an optimistic-concurrency failure when the
// caller's expected control revision no longer matches the current control
// state revision.
type ControlConflictError struct {
	Expected int64
	Current  int64
}

func (e ControlConflictError) Error() string {
	return fmt.Sprintf("control revision conflict: expected=%d current=%d", e.Expected, e.Current)
}

// ValidationError reports that a runtime update failed policy validation and
// was therefore not activated or audited.
type ValidationError struct {
	Errors []string
}

func (e ValidationError) Error() string {
	return "policy validation failed: " + strings.Join(e.Errors, "; ")
}
