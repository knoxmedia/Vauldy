package publication

import (
	"strings"
	"testing"
)

func graphNode(step StepType, generation int64) PlanNode {
	return PlanNode{Step: step, Generation: generation}
}
func graphEdge(step, dependsOn StepType, generation int64, kind DependencyKind) Dependency {
	return Dependency{Step: step, DependsOn: stepPtr(dependsOn), Generation: generation, DependsOnGeneration: generation, Kind: kind}
}

func TestPlanGraphRejectsInvalidTopology(t *testing.T) {
	base := PlanGraph{Nodes: []PlanNode{graphNode(StepMediaVisible, 1), graphNode(StepPreview, 1)}, Edges: []Dependency{graphEdge(StepPreview, StepMediaVisible, 1, DependencySuccess)}}
	tests := []struct {
		name     string
		graph    PlanGraph
		contains string
	}{
		{"unknown node", PlanGraph{Nodes: []PlanNode{graphNode("mystery", 1)}}, "unknown node"},
		{"duplicate logical node", PlanGraph{Nodes: []PlanNode{graphNode(StepPreview, 1), graphNode(StepPreview, 1)}}, "duplicate node"},
		{"unknown dependency", PlanGraph{Nodes: base.Nodes, Edges: []Dependency{graphEdge(StepPreview, StepMediaVisible, 1, "maybe")}}, "unknown dependency kind"},
		{"missing endpoint", PlanGraph{Nodes: base.Nodes, Edges: []Dependency{graphEdge(StepPreview, StepAIAnalysis, 1, DependencySuccess)}}, "missing endpoint"},
		{"cross generation", PlanGraph{Nodes: []PlanNode{graphNode(StepPreview, 1), graphNode(StepMediaVisible, 2)}, Edges: []Dependency{{Step: StepPreview, DependsOn: stepPtr(StepMediaVisible), Generation: 1, DependsOnGeneration: 2, Kind: DependencySuccess}}}, "cross-generation"},
		{"self edge", PlanGraph{Nodes: []PlanNode{graphNode(StepPreview, 1)}, Edges: []Dependency{graphEdge(StepPreview, StepPreview, 1, DependencySuccess)}}, "self-edge"},
		{"duplicate edge", PlanGraph{Nodes: base.Nodes, Edges: []Dependency{graphEdge(StepPreview, StepMediaVisible, 1, DependencySuccess), graphEdge(StepPreview, StepMediaVisible, 1, DependencySuccess)}}, "duplicate edge"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePlanGraph(tc.graph)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("err=%v want containing %q", err, tc.contains)
			}
		})
	}
}

func TestPlanGraphRequiresMediaVisibleAndStrictSuccessEdges(t *testing.T) {
	graph := PlanGraph{Nodes: []PlanNode{
		graphNode(StepMediaVisible, 3), graphNode(StepSubtitleExtract, 3), graphNode(StepAtrackExtract, 3), graphNode(StepSubtitleRecognize, 3), graphNode(StepAIAnalysis, 3),
	}, Edges: []Dependency{
		graphEdge(StepSubtitleExtract, StepMediaVisible, 3, DependencySuccess), graphEdge(StepAtrackExtract, StepMediaVisible, 3, DependencySuccess), graphEdge(StepSubtitleRecognize, StepMediaVisible, 3, DependencySuccess), graphEdge(StepAIAnalysis, StepMediaVisible, 3, DependencySuccess),
		graphEdge(StepSubtitleRecognize, StepSubtitleExtract, 3, DependencySuccess), graphEdge(StepSubtitleRecognize, StepAtrackExtract, 3, DependencySuccess), graphEdge(StepAIAnalysis, StepSubtitleRecognize, 3, DependencySuccess),
	}}
	if err := ValidatePlanGraph(graph); err != nil {
		t.Fatal(err)
	}
	graph.Edges[4].Kind = DependencyTerminal
	if err := ValidatePlanGraph(graph); err == nil || !strings.Contains(err.Error(), "forbidden edge") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanGraphRejectsForbiddenKnownEdge(t *testing.T) {
	graph := PlanGraph{Nodes: []PlanNode{graphNode(StepMediaVisible, 1), graphNode(StepPreview, 1), graphNode(StepScrape, 1)}, Edges: []Dependency{
		graphEdge(StepPreview, StepMediaVisible, 1, DependencySuccess), graphEdge(StepScrape, StepMediaVisible, 1, DependencySuccess), graphEdge(StepPreview, StepScrape, 1, DependencySuccess),
	}}
	if err := ValidatePlanGraph(graph); err == nil || !strings.Contains(err.Error(), "forbidden edge") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanGraphVisibilityRequiresSuccess(t *testing.T) {
	graph := PlanGraph{Nodes: []PlanNode{graphNode(StepMediaVisible, 1), graphNode(StepPreview, 1)}, Edges: []Dependency{graphEdge(StepPreview, StepMediaVisible, 1, DependencyTerminal)}}
	if err := ValidatePlanGraph(graph); err == nil || !strings.Contains(err.Error(), "forbidden edge") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanGraphRejectsCycleDeterministicallyWithinAllowedRegistry(t *testing.T) {
	registry := map[allowedEdge]map[DependencyKind]bool{}
	for pair, kinds := range allowedPhase1Edges {
		copyKinds := map[DependencyKind]bool{}
		for kind, allowed := range kinds {
			copyKinds[kind] = allowed
		}
		registry[pair] = copyKinds
	}
	registry[allowedEdge{StepMediaVisible, StepPreview}] = map[DependencyKind]bool{DependencySuccess: true}
	graph := PlanGraph{Nodes: []PlanNode{graphNode(StepMediaVisible, 1), graphNode(StepPreview, 1)}, Edges: []Dependency{graphEdge(StepPreview, StepMediaVisible, 1, DependencySuccess), graphEdge(StepMediaVisible, StepPreview, 1, DependencySuccess)}}
	if err := validatePlanGraphWithRegistry(graph, registry); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanGraphV3RejectsHistoricalScrapeCoreEdge(t *testing.T) {
	graph := PlanGraph{Nodes: []PlanNode{graphNode(StepPoster, 1), graphNode(StepMediaVisible, 1), graphNode(StepScrape, 1)}, Edges: []Dependency{graphEdge(StepScrape, StepMediaVisible, 1, DependencySuccess), graphEdge(StepScrape, StepPoster, 1, DependencyTerminal)}}
	if err := ValidatePlanGraph(graph); err == nil || !strings.Contains(err.Error(), "forbidden edge") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanGraphV3RejectsLegacyLogicalNodes(t *testing.T) {
	for _, step := range []StepType{StepSubtitle, StepAtrack, StepKeyframe} {
		if err := ValidatePlanGraph(PlanGraph{Nodes: []PlanNode{graphNode(step, 1)}}); err == nil || !strings.Contains(err.Error(), "unknown node") {
			t.Fatalf("step=%s err=%v", step, err)
		}
	}
}

func TestLegacyV2GraphCompatibilityAcceptsLegacyLogicalNodes(t *testing.T) {
	for _, step := range []StepType{StepSubtitle, StepAtrack, StepKeyframe} {
		if err := validateLegacyV2Graph(PlanGraph{Nodes: []PlanNode{graphNode(step, 1)}}); err != nil {
			t.Fatalf("step=%s err=%v", step, err)
		}
	}
}

func TestLegacyV2GraphRejectsBasicInvalidity(t *testing.T) {
	baseNodes := []PlanNode{graphNode(StepScrape, 1), graphNode(StepPoster, 1)}
	valid := graphEdge(StepScrape, StepPoster, 1, DependencySuccess)
	cases := []struct {
		name  string
		graph PlanGraph
		want  string
	}{
		{"unknown", PlanGraph{Nodes: []PlanNode{graphNode("mystery", 1)}}, "unknown node"},
		{"duplicate node", PlanGraph{Nodes: []PlanNode{graphNode(StepPoster, 1), graphNode(StepPoster, 1)}}, "duplicate node"},
		{"missing endpoint", PlanGraph{Nodes: baseNodes, Edges: []Dependency{graphEdge(StepScrape, StepThumbnail, 1, DependencySuccess)}}, "missing endpoint"},
		{"malformed endpoint", PlanGraph{Nodes: baseNodes, Edges: []Dependency{{Step: StepScrape, Generation: 1, Kind: DependencySuccess}}}, "malformed"},
		{"cross generation", PlanGraph{Nodes: []PlanNode{graphNode(StepScrape, 1), graphNode(StepPoster, 2)}, Edges: []Dependency{{Step: StepScrape, DependsOn: stepPtr(StepPoster), Generation: 1, DependsOnGeneration: 2, Kind: DependencySuccess}}}, "cross-generation"},
		{"self edge", PlanGraph{Nodes: []PlanNode{graphNode(StepPoster, 1)}, Edges: []Dependency{graphEdge(StepPoster, StepPoster, 1, DependencySuccess)}}, "self-edge"},
		{"duplicate edge", PlanGraph{Nodes: baseNodes, Edges: []Dependency{valid, valid}}, "duplicate edge"},
		{"cycle", PlanGraph{Nodes: baseNodes, Edges: []Dependency{valid, graphEdge(StepPoster, StepScrape, 1, DependencySuccess)}}, "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLegacyV2Graph(tc.graph)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want=%q", err, tc.want)
			}
		})
	}
}

type fakeSourceRegistry map[StepType]EncryptedSourceContract

func (r fakeSourceRegistry) Contract(step StepType) (EncryptedSourceContract, bool) {
	v, ok := r[step]
	return v, ok
}

func TestEncryptedSourceContractPhaseBoundary(t *testing.T) {
	selected := []StepType{StepMediaVisible, StepPreview, StepScrape}
	if _, err := ValidateEncryptedSourceContracts(selected, true, nil); err == nil || !strings.Contains(err.Error(), "lacks validated") {
		t.Fatalf("nil registry err=%v", err)
	}
	registry := fakeSourceRegistry{StepPreview: {Strategy: EncryptedSourceStreamDecrypt, Validated: true}, StepScrape: {Strategy: EncryptedSourceDerivative, Validated: true}}
	got, err := ValidateEncryptedSourceContracts(selected, true, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[StepPreview].Strategy != EncryptedSourceStreamDecrypt || got[StepScrape].Strategy != EncryptedSourceDerivative {
		t.Fatalf("got=%v", got)
	}
	for _, contract := range []EncryptedSourceContract{{Strategy: "unknown", Validated: true}, {Strategy: EncryptedSourceStreamDecrypt, Validated: false}} {
		if _, err = ValidateEncryptedSourceContracts([]StepType{StepPreview}, true, fakeSourceRegistry{StepPreview: contract}); err == nil {
			t.Fatalf("accepted %+v", contract)
		}
	}
}

func TestEncryptedSourceContractRejectsDuplicateSelection(t *testing.T) {
	registry := fakeSourceRegistry{StepPreview: {Strategy: EncryptedSourceStreamDecrypt, Validated: true}}
	if _, err := ValidateEncryptedSourceContracts([]StepType{StepPreview, StepPreview}, true, registry); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
	}
}

func TestEncryptedSourceCoverageIncludesRetryableExecutors(t *testing.T) {
	selected := []StepType{StepPoster, StepScrape, StepPreview, StepSubtitleExtract, StepAtrackExtract, StepSubtitleRecognize, StepKeyframeExtract, StepPrepare, StepMediaVisible, StepEncrypt, StepPackage}
	for _, missing := range []StepType{StepPoster, StepScrape, StepPreview, StepSubtitleExtract, StepAtrackExtract, StepSubtitleRecognize, StepKeyframeExtract, StepPrepare} {
		t.Run(string(missing), func(t *testing.T) {
			registry := fakeSourceRegistry{}
			for _, step := range selected {
				if step != missing && requiresEncryptedSourceStrategy(step) {
					registry[step] = EncryptedSourceContract{Strategy: EncryptedSourceStreamDecrypt, Validated: true}
				}
			}
			_, err := ValidateEncryptedSourceContracts(selected, true, registry)
			if err == nil || !strings.Contains(err.Error(), string(missing)) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
