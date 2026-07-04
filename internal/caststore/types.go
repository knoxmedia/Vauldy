package caststore

// Occupation codes (SRS §8.1).
const (
	OccActor           = "actor"
	OccDirector        = "director"
	OccWriter          = "writer"
	OccProducer        = "producer"
	OccCinematographer = "cinematographer"
	OccEditor          = "editor"
	OccArtDirector     = "art_director"
	OccComposer        = "composer"
	OccCostume         = "costume"
	OccOther           = "other"
)

// RoleType codes for actors (SRS §8.2).
const (
	RoleLeading    = "leading"
	RoleSupporting = "supporting"
	RoleCameo      = "cameo"
	RoleSpecial    = "special"
)

// ValidOccupations lists supported occupation values.
var ValidOccupations = map[string]bool{
	OccActor: true, OccDirector: true, OccWriter: true, OccProducer: true,
	OccCinematographer: true, OccEditor: true, OccArtDirector: true,
	OccComposer: true, OccCostume: true, OccOther: true,
}

// ValidRoleTypes lists supported actor role types.
var ValidRoleTypes = map[string]bool{
	RoleLeading: true, RoleSupporting: true, RoleCameo: true, RoleSpecial: true,
}
