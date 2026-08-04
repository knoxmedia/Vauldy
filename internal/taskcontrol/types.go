package taskcontrol

// SourceMapping maps a Phase 1-3 implementation source to a public stable type.
// Kind is the source table/entity (e.g., "post_ingest_task", "transcode_task").
// InternalType is the internal task type within that source (empty string
// for sources that serve a single type like transcode_task).
type SourceMapping struct {
	Kind         string `json:"kind"`
	InternalType string `json:"internal_type,omitempty"`
}

// ColumnSpec describes a column shown in the task list for this type.
type ColumnSpec struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// FilterSpec describes an available filter for this task type.
type FilterSpec struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Values []string `json:"values,omitempty"`
}

// TaskSpec is the immutable descriptor for a single task type.
type TaskSpec struct {
	Type           string          `json:"type"`
	Group          string          `json:"group"`
	Route          string          `json:"route"`
	Family         string          `json:"family"`
	SourceMappings []SourceMapping `json:"source_mappings"`
	Columns        []ColumnSpec    `json:"columns"`
	Filters        []FilterSpec    `json:"filters"`
	Capabilities   []string        `json:"capabilities"`
	Available      bool            `json:"available"`
}

// TaskGroup groups related task types under a label.
type TaskGroup struct {
	Label      string     `json:"label"`
	Selectable bool       `json:"selectable"`
	Types      []TaskSpec `json:"types"`
}

// Registry holds the ordered collection of task groups and their types.
type Registry struct {
	Groups []TaskGroup `json:"groups"`
}
