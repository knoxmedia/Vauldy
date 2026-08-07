package publication

import (
	"errors"
	"fmt"
	"sort"
)

var ErrCapabilityUnavailable = errors.New("publication planner: required capability unavailable")

var knownPhase1Nodes = map[StepType]bool{
	StepPoster: true, StepThumbnail: true, StepScrape: true, StepPreview: true, StepEncrypt: true, StepPrepare: true,
	StepPackage: true, StepPretranscode: true, StepMetadata: true, StepMediaVisible: true,
	StepSubtitleExtract: true, StepAtrackExtract: true, StepSubtitleRecognize: true, StepKeyframeExtract: true, StepAIAnalysis: true,
	// Phase 5 media task types
	StepLyricRecognize: true, StepAudioAnalysis: true, StepPhotoClassify: true, StepPhotoGeocode: true,
	StepPhotoFace: true, StepImageOCR: true, StepDocumentConvert: true, StepDocumentFulltext: true,
	StepPersonScrape: true, StepArtworkCover: true,
}

var knownPhase5Nodes = map[StepType]bool{
	StepLyricRecognize: true, StepAudioAnalysis: true, StepPhotoClassify: true, StepPhotoGeocode: true,
	StepPhotoFace: true, StepImageOCR: true, StepDocumentConvert: true, StepDocumentFulltext: true,
	StepPersonScrape: true, StepArtworkCover: true,
}

type phase1TaskDescriptor struct{ async, retryAfterPlaintextRetirement bool }

var phase1TaskDescriptors = map[StepType]phase1TaskDescriptor{
	StepPoster: {retryAfterPlaintextRetirement: true}, StepThumbnail: {retryAfterPlaintextRetirement: true}, StepScrape: {async: true, retryAfterPlaintextRetirement: true}, StepPreview: {async: true, retryAfterPlaintextRetirement: true},
	StepSubtitleExtract: {async: true, retryAfterPlaintextRetirement: true}, StepAtrackExtract: {async: true, retryAfterPlaintextRetirement: true}, StepSubtitleRecognize: {async: true, retryAfterPlaintextRetirement: true}, StepKeyframeExtract: {async: true, retryAfterPlaintextRetirement: true}, StepAIAnalysis: {async: true, retryAfterPlaintextRetirement: true}, StepPrepare: {async: true, retryAfterPlaintextRetirement: true},
	// media_visible is queue-less. Encrypt/package establish retirement and are not reopened after it.
	StepMediaVisible: {}, StepEncrypt: {}, StepPackage: {async: true}, StepPretranscode: {async: true}, StepMetadata: {},
}

func requiresEncryptedSourceStrategy(step StepType) bool {
	return phase1TaskDescriptors[step].retryAfterPlaintextRetirement
}

var asyncPhase1Nodes = map[StepType]bool{
	StepScrape: true, StepPreview: true, StepPrepare: true, StepSubtitleExtract: true, StepAtrackExtract: true,
	StepSubtitleRecognize: true, StepKeyframeExtract: true, StepAIAnalysis: true, StepPackage: true, StepPretranscode: true,
	// Phase 5 async nodes
	StepLyricRecognize: true, StepAudioAnalysis: true, StepPhotoClassify: true, StepPhotoGeocode: true, StepPhotoFace: true,
	StepImageOCR: true, StepDocumentConvert: true, StepDocumentFulltext: true,
}

type allowedEdge struct{ from, to StepType }

var allowedPhase1Edges = map[allowedEdge]map[DependencyKind]bool{
	{StepScrape, StepMediaVisible}: {DependencySuccess: true}, {StepPreview, StepMediaVisible}: {DependencySuccess: true},
	{StepPrepare, StepMediaVisible}: {DependencySuccess: true}, {StepSubtitleExtract, StepMediaVisible}: {DependencySuccess: true},
	{StepAtrackExtract, StepMediaVisible}: {DependencySuccess: true}, {StepSubtitleRecognize, StepMediaVisible}: {DependencySuccess: true},
	{StepKeyframeExtract, StepMediaVisible}: {DependencySuccess: true}, {StepAIAnalysis, StepMediaVisible}: {DependencySuccess: true},
	{StepPackage, StepMediaVisible}: {DependencySuccess: true}, {StepPretranscode, StepMediaVisible}: {DependencySuccess: true},
	{StepSubtitleRecognize, StepSubtitleExtract}: {DependencySuccess: true}, {StepSubtitleRecognize, StepAtrackExtract}: {DependencySuccess: true},
	{StepAIAnalysis, StepSubtitleRecognize}: {DependencySuccess: true},
	{StepEncrypt, StepPoster}:                  {DependencySuccess: true}, {StepEncrypt, StepThumbnail}: {DependencySuccess: true},
	// Phase 5 audio edges
	{StepLyricRecognize, StepMediaVisible}: {DependencySuccess: true},
	{StepAudioAnalysis, StepMediaVisible}:  {DependencySuccess: true},
	{StepAIAnalysis, StepLyricRecognize}:   {DependencySuccess: true},
	{StepAIAnalysis, StepAudioAnalysis}:    {DependencySuccess: true},
	// Phase 5 image edges
	{StepPhotoClassify, StepMediaVisible}: {DependencySuccess: true},
	{StepPhotoGeocode, StepMediaVisible}:  {DependencySuccess: true},
	{StepPhotoFace, StepMediaVisible}:     {DependencySuccess: true},
	{StepImageOCR, StepMediaVisible}:      {DependencySuccess: true},
	{StepAIAnalysis, StepPhotoClassify}:   {DependencySuccess: true},
	{StepAIAnalysis, StepPhotoGeocode}:    {DependencySuccess: true},
	{StepAIAnalysis, StepPhotoFace}:       {DependencySuccess: true},
	// Phase 5 document edges
	{StepDocumentConvert, StepMediaVisible}:  {DependencySuccess: true},
	{StepDocumentFulltext, StepMediaVisible}: {DependencySuccess: true},
	{StepImageOCR, StepMediaVisible}:         {DependencySuccess: true},
	{StepAIAnalysis, StepDocumentFulltext}:   {DependencySuccess: true},
	{StepImageOCR, StepDocumentFulltext}:     {DependencySuccess: true},
	{StepDocumentConvert, StepDocumentFulltext}: {DependencySuccess: true},
	{StepDocumentFulltext, StepDocumentConvert}: {DependencySuccess: true},
	// Phase 5 common edges
	{StepPersonScrape, StepMediaVisible}: {DependencySuccess: true},
	{StepArtworkCover, StepMediaVisible}: {DependencySuccess: true},
}

type logicalNode struct {
	step       StepType
	generation int64
}
type logicalEdge struct {
	from, to logicalNode
	kind     DependencyKind
}

func ValidatePlanGraph(graph PlanGraph) error {
	return validatePlanGraphWithRegistry(graph, allowedPhase1Edges)
}

func validatePlanGraphWithRegistry(graph PlanGraph, registry map[allowedEdge]map[DependencyKind]bool) error {
	nodes := make(map[logicalNode]PlanNode, len(graph.Nodes))
	ordered := make([]logicalNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if !knownPhase1Nodes[node.Step] {
			return fmt.Errorf("plan graph: unknown node type %q", node.Step)
		}
		if node.Generation <= 0 {
			return fmt.Errorf("plan graph: malformed node %q generation %d", node.Step, node.Generation)
		}
		key := logicalNode{node.Step, node.Generation}
		if _, exists := nodes[key]; exists {
			return fmt.Errorf("plan graph: duplicate node %q generation %d", node.Step, node.Generation)
		}
		nodes[key] = node
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].generation != ordered[j].generation {
			return ordered[i].generation < ordered[j].generation
		}
		return ordered[i].step < ordered[j].step
	})
	edges := make(map[logicalEdge]bool, len(graph.Edges))
	adjacency := make(map[logicalNode][]logicalNode)
	for _, edge := range graph.Edges {
		if edge.Kind != DependencySuccess && edge.Kind != DependencyTerminal {
			return fmt.Errorf("plan graph: unknown dependency kind %q", edge.Kind)
		}
		if edge.DependsOn == nil || edge.Step == "" {
			return errors.New("plan graph: malformed/missing endpoint")
		}
		fromGen, toGen := edge.Generation, edge.DependsOnGeneration
		if fromGen <= 0 || toGen <= 0 {
			return errors.New("plan graph: malformed/missing endpoint generation")
		}
		if fromGen != toGen {
			return fmt.Errorf("plan graph: cross-generation edge %s/%d -> %s/%d", edge.Step, fromGen, *edge.DependsOn, toGen)
		}
		from, to := logicalNode{edge.Step, fromGen}, logicalNode{*edge.DependsOn, toGen}
		if _, ok := nodes[from]; !ok {
			return fmt.Errorf("plan graph: missing endpoint %s/%d", from.step, from.generation)
		}
		if _, ok := nodes[to]; !ok {
			return fmt.Errorf("plan graph: missing endpoint %s/%d", to.step, to.generation)
		}
		if from == to {
			return fmt.Errorf("plan graph: self-edge %s/%d", from.step, from.generation)
		}
		key := logicalEdge{from, to, edge.Kind}
		if edges[key] {
			return fmt.Errorf("plan graph: duplicate edge %s -> %s (%s)", from.step, to.step, edge.Kind)
		}
		edges[key] = true
		if !registry[allowedEdge{from.step, to.step}][edge.Kind] {
			return fmt.Errorf("plan graph: forbidden edge %s -> %s (%s)", from.step, to.step, edge.Kind)
		}
		if requiresSuccessEdge(from.step, to.step) && edge.Kind != DependencySuccess {
			return fmt.Errorf("plan graph: %s -> %s requires success dependency", to.step, from.step)
		}
		adjacency[from] = append(adjacency[from], to)
	}
	for node := range nodes {
		if asyncPhase1Nodes[node.step] && !edges[logicalEdge{node, logicalNode{StepMediaVisible, node.generation}, DependencySuccess}] {
			return fmt.Errorf("plan graph: asynchronous node %s requires media_visible dependency", node.step)
		}
	}
	for node := range adjacency {
		sort.Slice(adjacency[node], func(i, j int) bool { return adjacency[node][i].step < adjacency[node][j].step })
	}
	state := map[logicalNode]uint8{}
	var visit func(logicalNode) error
	visit = func(node logicalNode) error {
		if state[node] == 1 {
			return fmt.Errorf("plan graph: cycle at %s/%d", node.step, node.generation)
		}
		if state[node] == 2 {
			return nil
		}
		state[node] = 1
		for _, next := range adjacency[node] {
			if err := visit(next); err != nil {
				return err
			}
		}
		state[node] = 2
		return nil
	}
	for _, node := range ordered {
		if err := visit(node); err != nil {
			return err
		}
	}
	return nil
}

func requiresSuccessEdge(from, to StepType) bool {
	return (from == StepSubtitleRecognize && (to == StepSubtitleExtract || to == StepAtrackExtract)) || (from == StepAIAnalysis && to == StepSubtitleRecognize)
}

// Task 9 supplies concrete source-strategy registrations. Task 6 intentionally
// fails closed for cleanup-eligible plans when this typed registry is absent.
func ValidateEncryptedSourceContracts(selected []StepType, cleanupEligible bool, registry EncryptedSourceRegistry) (map[StepType]EncryptedSourceContract, error) {
	out := make(map[StepType]EncryptedSourceContract)
	if !cleanupEligible {
		return out, nil
	}
	seen := make(map[StepType]bool, len(selected))
	for _, step := range selected {
		if seen[step] {
			return nil, fmt.Errorf("plan graph: duplicate selected task %s", step)
		}
		seen[step] = true
		if !requiresEncryptedSourceStrategy(step) {
			continue
		}
		contract, ok := EncryptedSourceContract{}, false
		if registry != nil {
			contract, ok = registry.Contract(step)
		}
		if !ok || !contract.Validated || (contract.Strategy != EncryptedSourceStreamDecrypt && contract.Strategy != EncryptedSourceMaterializeTemp && contract.Strategy != EncryptedSourceDerivative) {
			return nil, fmt.Errorf("plan graph: cleanup-eligible task %s lacks validated encrypted-source strategy", step)
		}
		out[step] = contract
	}
	return out, nil
}

func validateLegacyV2Graph(graph PlanGraph) error {
	known := map[StepType]bool{StepPoster: true, StepThumbnail: true, StepScrape: true, StepPreview: true, StepEncrypt: true, StepPrepare: true, StepKeyframe: true, StepSubtitle: true, StepAtrack: true, StepMediaVisible: true}
	nodes := make(map[logicalNode]bool, len(graph.Nodes))
	ordered := make([]logicalNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if !known[node.Step] {
			return fmt.Errorf("legacy v2 graph: unknown node %q", node.Step)
		}
		if node.Generation <= 0 {
			return fmt.Errorf("legacy v2 graph: malformed node %q", node.Step)
		}
		key := logicalNode{node.Step, node.Generation}
		if nodes[key] {
			return fmt.Errorf("legacy v2 graph: duplicate node %s/%d", node.Step, node.Generation)
		}
		nodes[key] = true
		ordered = append(ordered, key)
	}
	edges := map[logicalEdge]bool{}
	adjacency := map[logicalNode][]logicalNode{}
	for _, edge := range graph.Edges {
		if edge.Kind != DependencySuccess && edge.Kind != DependencyTerminal {
			return fmt.Errorf("legacy v2 graph: unknown dependency kind %q", edge.Kind)
		}
		if edge.Step == "" || edge.DependsOn == nil || edge.Generation <= 0 || edge.DependsOnGeneration <= 0 {
			return errors.New("legacy v2 graph: malformed endpoint")
		}
		if edge.Generation != edge.DependsOnGeneration {
			return errors.New("legacy v2 graph: cross-generation edge")
		}
		from, to := logicalNode{edge.Step, edge.Generation}, logicalNode{*edge.DependsOn, edge.DependsOnGeneration}
		if !nodes[from] || !nodes[to] {
			return errors.New("legacy v2 graph: missing endpoint")
		}
		if from == to {
			return errors.New("legacy v2 graph: self-edge")
		}
		key := logicalEdge{from, to, edge.Kind}
		if edges[key] {
			return errors.New("legacy v2 graph: duplicate edge")
		}
		edges[key] = true
		adjacency[from] = append(adjacency[from], to)
	}
	state := map[logicalNode]uint8{}
	var visit func(logicalNode) error
	visit = func(node logicalNode) error {
		if state[node] == 1 {
			return errors.New("legacy v2 graph: cycle")
		}
		if state[node] == 2 {
			return nil
		}
		state[node] = 1
		for _, next := range adjacency[node] {
			if err := visit(next); err != nil {
				return err
			}
		}
		state[node] = 2
		return nil
	}
	for _, node := range ordered {
		if err := visit(node); err != nil {
			return err
		}
	}
	return nil
}
