// Package evidence provides PARADOX's typed epistemic layer over the symbolic
// truth-maintenance kernel in core. Observations are roots, assumptions are
// explicit roots, and claims are derived facts whose justifications name the
// evidence and assumptions they depend on. Each claim has a positive
// contradiction guard. Retracting that guard invalidates the claim and every
// conclusion that depends on it.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qeinstein/paradox/core"
)

type Kind string

const (
	KindObservation   Kind = "observation"
	KindAssumption    Kind = "assumption"
	KindClaim         Kind = "claim"
	KindContradiction Kind = "contradiction"
	KindDecision      Kind = "decision"
	KindArtifact      Kind = "artifact"
)

type Source string

const (
	SourceExperiment          Source = "experiment"
	SourceTool                Source = "tool"
	SourceUser                Source = "user"
	SourceModelInterpretation Source = "model_interpretation"
	SourceModelAssumption     Source = "model_assumption"
	SourceVerification        Source = "verification"
	SourceSystem              Source = "system"
)

type Verification string

const (
	Unverified Verification = "unverified"
	Supported  Verification = "supported"
	Verified   Verification = "verified"
	Rejected   Verification = "rejected"
)

type ArtifactRef struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

type ObservationInput struct {
	Text       string         `json:"text"`
	ClaimKey   string         `json:"claim_key,omitempty"`
	ClaimValue string         `json:"claim_value,omitempty"`
	Subject    string         `json:"subject,omitempty"`
	Predicate  string         `json:"predicate,omitempty"`
	Object     string         `json:"object,omitempty"`
	Polarity   string         `json:"polarity,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	Provenance map[string]any `json:"provenance,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Artifacts  []ArtifactRef  `json:"artifacts,omitempty"`
	Source     Source         `json:"source,omitempty"`
}

type AssumptionInput struct {
	Text       string         `json:"text"`
	Kind       string         `json:"kind,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type ClaimInput struct {
	Text        string            `json:"text"`
	ClaimKey    string            `json:"claim_key,omitempty"`
	ClaimValue  string            `json:"claim_value,omitempty"`
	Subject     string            `json:"subject,omitempty"`
	Predicate   string            `json:"predicate,omitempty"`
	Object      string            `json:"object,omitempty"`
	Polarity    string            `json:"polarity,omitempty"`
	Confidence  float64           `json:"confidence,omitempty"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
	Assumptions []AssumptionInput `json:"assumptions,omitempty"`
	Metadata    map[string]any    `json:"metadata,omitempty"`
}

type Node struct {
	ID            string         `json:"id"`
	Kind          Kind           `json:"kind"`
	Text          string         `json:"text"`
	Source        Source         `json:"source,omitempty"`
	Verification  Verification   `json:"verification"`
	Epistemic     string         `json:"epistemic,omitempty"`
	Confidence    float64        `json:"confidence"`
	Status        float64        `json:"status"`
	Active        bool           `json:"active"`
	ClaimKey      string         `json:"claim_key,omitempty"`
	ClaimValue    string         `json:"claim_value,omitempty"`
	Subject       string         `json:"subject,omitempty"`
	Predicate     string         `json:"predicate,omitempty"`
	Object        string         `json:"object,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	EvidenceIDs   []string       `json:"evidence_ids,omitempty"`
	AssumptionIDs []string       `json:"assumption_ids,omitempty"`
	GuardID       string         `json:"guard_id,omitempty"`
	Artifacts     []ArtifactRef  `json:"artifacts,omitempty"`
	Internal      bool           `json:"internal,omitempty"`
	CreatedAt     int64          `json:"created_at"`
}

type Edge struct {
	From             string `json:"from"`
	To               string `json:"to"`
	Negated          bool   `json:"negated"`
	JustificationSet int    `json:"justification_set"`
}

type Contradiction struct {
	ID           string `json:"id"`
	ClaimID      string `json:"claim_id"`
	AssumptionID string `json:"assumption_id,omitempty"`
	EvidenceID   string `json:"evidence_id,omitempty"`
	Reason       string `json:"reason"`
	At           int64  `json:"at"`
}

type Retraction struct {
	ID      string `json:"id"`
	NodeID  string `json:"node_id"`
	Reason  string `json:"reason"`
	Cause   string `json:"cause"`
	Correct bool   `json:"correct"`
	At      int64  `json:"at"`
}

type VerificationRecord struct {
	ID           string `json:"id"`
	PlanID       string `json:"plan_id"`
	ClaimID      string `json:"claim_id"`
	AssumptionID string `json:"assumption_id,omitempty"`
	EvidenceID   string `json:"evidence_id,omitempty"`
	Method       string `json:"method"`
	Contradicted bool   `json:"contradicted"`
	Correct      bool   `json:"correct"`
	Reason       string `json:"reason,omitempty"`
	At           int64  `json:"at"`
}

type Stats struct {
	Nodes                int     `json:"nodes"`
	Observations         int     `json:"observations"`
	Assumptions          int     `json:"assumptions"`
	Claims               int     `json:"claims"`
	ActiveClaims         int     `json:"active_claims"`
	UnsupportedClaims    int     `json:"unsupported_claims"`
	InvalidClaims        int     `json:"invalid_claims"`
	Contradictions       int     `json:"contradictions"`
	Retractions          int     `json:"retractions"`
	Verifications        int     `json:"verifications"`
	UnsupportedRate      float64 `json:"unsupported_claim_rate"`
	IncorrectRetractions int     `json:"incorrect_retractions"`
}

type Snapshot struct {
	RunID          string               `json:"run_id"`
	Nodes          []Node               `json:"nodes"`
	Edges          []Edge               `json:"edges"`
	Contradictions []Contradiction      `json:"contradictions"`
	Retractions    []Retraction         `json:"retractions"`
	Verifications  []VerificationRecord `json:"verifications"`
	Stats          Stats                `json:"stats"`
	GeneratedAt    int64                `json:"generated_at"`
}

type ClaimResult struct {
	Claim       Node   `json:"claim"`
	Assumptions []Node `json:"assumptions,omitempty"`
	GuardID     string `json:"guard_id,omitempty"`
}

type ContradictionResult struct {
	Contradiction Contradiction `json:"contradiction"`
	Invalidated   []string      `json:"invalidated"`
	Retracted     []string      `json:"retracted"`
}

// Kernel is a single run's evidence store. It is safe for concurrent reads and
// serialized writes; the agent loop writes, the server/UI reads snapshots.
type Kernel struct {
	mu             sync.Mutex
	runID          string
	engine         *core.Engine
	counter        int
	nodes          map[string]Node
	contradictions []Contradiction
	retractions    []Retraction
	verifications  []VerificationRecord
}

func New(runID string) *Kernel {
	return &Kernel{
		runID:  runID,
		engine: core.NewEngine(),
		nodes:  make(map[string]Node),
	}
}

func (k *Kernel) Engine() *core.Engine { return k.engine }

func (k *Kernel) nextID(prefix string) string {
	k.counter++
	return fmt.Sprintf("%s-%04d", prefix, k.counter)
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func mergeMaps(groups ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, group := range groups {
		for key, value := range group {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hashJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (k *Kernel) factPayload(kind Kind, text string, metadata map[string]any) map[string]any {
	payload := map[string]any{
		"kind": string(kind),
		"text": text,
	}
	if metadata != nil {
		payload["metadata"] = cloneMap(metadata)
	}
	return payload
}

func provenanceMap(source Source, provenance map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range provenance {
		out[key] = value
	}
	if source != "" {
		out["source_type"] = string(source)
	}
	return out
}

func (k *Kernel) registerNode(node Node) {
	k.nodes[node.ID] = node
}

func (k *Kernel) AddObservation(in ObservationInput) (Node, error) {
	if strings.TrimSpace(in.Text) == "" {
		return Node{}, errors.New("observation text is required")
	}
	if in.Confidence <= 0 {
		in.Confidence = 1.0
	}
	source := in.Source
	if source == "" {
		source = SourceTool
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	id := k.nextID("obs")
	provenance := provenanceMap(source, in.Provenance)
	metadata := mergeMaps(in.Metadata, map[string]any{
		"kind":                string(KindObservation),
		"source_type":         string(source),
		"verification_status": string(Verified),
		"epistemic_status":    "direct_observation",
	})
	payload := k.factPayload(KindObservation, in.Text, in.Metadata)
	payload["claim_key"] = in.ClaimKey
	payload["claim_value"] = in.ClaimValue
	payload["subject"] = in.Subject
	payload["predicate"] = in.Predicate
	payload["object"] = in.Object
	payload["polarity"] = firstNonEmpty(in.Polarity, "positive")
	if len(in.Artifacts) > 0 {
		payload["artifacts"] = in.Artifacts
	}
	fact := &core.Fact{
		ID:            id,
		Payload:       payload,
		Metadata:      mergeMaps(metadata, map[string]any{"_provenance": provenance}),
		IsRoot:        true,
		ManualStatus:  core.Status(in.Confidence),
		AssertionKind: core.AssertionKindEmpirical,
	}
	if err := k.engine.AssertFact(fact); err != nil {
		return Node{}, err
	}
	node := Node{
		ID: id, Kind: KindObservation, Text: in.Text, Source: source,
		Verification: Verified, Epistemic: "direct_observation",
		Confidence: in.Confidence, Status: in.Confidence, Active: true,
		ClaimKey: in.ClaimKey, ClaimValue: in.ClaimValue,
		Subject: in.Subject, Predicate: in.Predicate, Object: in.Object,
		Metadata: cloneMap(metadata), Artifacts: append([]ArtifactRef(nil), in.Artifacts...),
		CreatedAt: time.Now().UnixMilli(),
	}
	k.registerNode(node)
	return node, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (k *Kernel) AddAssumption(in AssumptionInput) (Node, error) {
	if strings.TrimSpace(in.Text) == "" {
		return Node{}, errors.New("assumption text is required")
	}
	if in.Confidence <= 0 {
		in.Confidence = 0.8
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	id := k.nextID("asm")
	metadata := mergeMaps(in.Metadata, map[string]any{
		"kind":                string(KindAssumption),
		"assumption_kind":     firstNonEmpty(in.Kind, "unspecified"),
		"source_type":         string(SourceModelAssumption),
		"verification_status": string(Unverified),
		"epistemic_status":    "explicit_assumption",
	})
	payload := k.factPayload(KindAssumption, in.Text, in.Metadata)
	payload["assumption_kind"] = firstNonEmpty(in.Kind, "unspecified")
	fact := &core.Fact{
		ID:            id,
		Payload:       payload,
		Metadata:      mergeMaps(metadata, map[string]any{"_provenance": provenanceMap(SourceModelAssumption, nil)}),
		IsRoot:        true,
		ManualStatus:  core.Status(in.Confidence),
		AssertionKind: core.AssertionKindEmpirical,
	}
	if err := k.engine.AssertFact(fact); err != nil {
		return Node{}, err
	}
	node := Node{
		ID: id, Kind: KindAssumption, Text: in.Text, Source: SourceModelAssumption,
		Verification: Unverified, Epistemic: "explicit_assumption",
		Confidence: in.Confidence, Status: in.Confidence, Active: true,
		Metadata: cloneMap(metadata), CreatedAt: time.Now().UnixMilli(),
	}
	k.registerNode(node)
	return node, nil
}

func (k *Kernel) SubmitClaim(in ClaimInput) (*ClaimResult, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, errors.New("claim text is required")
	}
	if in.Confidence <= 0 {
		in.Confidence = 0.8
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	for _, evidenceID := range in.EvidenceIDs {
		node, ok := k.nodes[evidenceID]
		if !ok {
			return nil, fmt.Errorf("unknown evidence id %q", evidenceID)
		}
		if node.Kind != KindObservation && node.Kind != KindClaim {
			return nil, fmt.Errorf("evidence id %q is %s, not an observation or claim", evidenceID, node.Kind)
		}
	}

	claimID := k.nextID("claim")
	guardID := "guard-" + claimID
	guard := &core.Fact{
		ID:      guardID,
		Payload: k.factPayload(KindContradiction, "contradiction guard for "+claimID, nil),
		Metadata: map[string]any{
			"kind":                string(KindContradiction),
			"source_type":         string(SourceSystem),
			"verification_status": string(Verified),
			"guard_for":           claimID,
		},
		IsRoot:       true,
		ManualStatus: core.Valid,
	}
	if err := k.engine.AssertFact(guard); err != nil {
		return nil, err
	}
	k.registerNode(Node{
		ID: guardID, Kind: KindContradiction, Text: "contradiction guard for " + claimID,
		Source: SourceSystem, Verification: Verified, Epistemic: "guard",
		Confidence: 1, Status: 1, Active: true, GuardID: claimID, Internal: true,
		CreatedAt: time.Now().UnixMilli(),
	})

	assumptionIDs := []string{}
	assumptionNodes := []Node{}
	for _, assumption := range in.Assumptions {
		// AddAssumption takes the lock; do the minimal unlocked work here.
		node, err := k.addAssumptionLocked(assumption)
		if err != nil {
			return nil, err
		}
		assumptionIDs = append(assumptionIDs, node.ID)
		assumptionNodes = append(assumptionNodes, node)
	}

	deps := append([]string{}, in.EvidenceIDs...)
	deps = append(deps, assumptionIDs...)
	metadata := mergeMaps(in.Metadata, map[string]any{
		"kind":                string(KindClaim),
		"source_type":         string(SourceModelInterpretation),
		"verification_status": string(Unverified),
		"epistemic_status":    "hypothesis",
		"evidence_ids":        in.EvidenceIDs,
		"assumption_ids":      assumptionIDs,
		"guard_id":            guardID,
		"confidence_input":    in.Confidence,
	})
	if len(in.EvidenceIDs) > 0 || len(assumptionIDs) > 0 {
		metadata["epistemic_status"] = "supported_hypothesis"
	}
	payload := k.factPayload(KindClaim, in.Text, in.Metadata)
	payload["claim_key"] = in.ClaimKey
	payload["claim_value"] = in.ClaimValue
	payload["subject"] = in.Subject
	payload["predicate"] = in.Predicate
	payload["object"] = in.Object
	payload["polarity"] = firstNonEmpty(in.Polarity, "positive")
	payload["evidence_ids"] = in.EvidenceIDs
	payload["assumption_ids"] = assumptionIDs
	payload["guard_id"] = guardID
	if scope, ok := in.Metadata["scope"]; ok {
		payload["scope"] = scope
	}
	fact := &core.Fact{
		ID:                claimID,
		Payload:           payload,
		Metadata:          mergeMaps(metadata, map[string]any{"_provenance": provenanceMap(SourceModelInterpretation, nil)}),
		JustificationSets: [][]string{append(deps, guardID)},
		AssertionKind:     core.AssertionKindEmpirical,
	}
	if err := k.engine.AssertFact(fact); err != nil {
		return nil, err
	}
	status := float64(k.engine.GetStatus(claimID))
	node := Node{
		ID: claimID, Kind: KindClaim, Text: in.Text, Source: SourceModelInterpretation,
		Verification: Unverified, Epistemic: fmt.Sprint(metadata["epistemic_status"]),
		Confidence: status, Status: status, Active: status >= 0.6,
		ClaimKey: in.ClaimKey, ClaimValue: in.ClaimValue,
		Subject: in.Subject, Predicate: in.Predicate, Object: in.Object,
		Metadata: cloneMap(metadata), EvidenceIDs: append([]string(nil), in.EvidenceIDs...),
		AssumptionIDs: assumptionIDs, GuardID: guardID, CreatedAt: time.Now().UnixMilli(),
	}
	k.registerNode(node)
	return &ClaimResult{Claim: node, Assumptions: assumptionNodes, GuardID: guardID}, nil
}

// addAssumptionLocked is the lock-held implementation used by SubmitClaim.
func (k *Kernel) addAssumptionLocked(in AssumptionInput) (Node, error) {
	if strings.TrimSpace(in.Text) == "" {
		return Node{}, errors.New("assumption text is required")
	}
	if in.Confidence <= 0 {
		in.Confidence = 0.8
	}
	id := k.nextID("asm")
	metadata := mergeMaps(in.Metadata, map[string]any{
		"kind":                string(KindAssumption),
		"assumption_kind":     firstNonEmpty(in.Kind, "unspecified"),
		"source_type":         string(SourceModelAssumption),
		"verification_status": string(Unverified),
		"epistemic_status":    "explicit_assumption",
	})
	payload := k.factPayload(KindAssumption, in.Text, in.Metadata)
	payload["assumption_kind"] = firstNonEmpty(in.Kind, "unspecified")
	fact := &core.Fact{
		ID:            id,
		Payload:       payload,
		Metadata:      mergeMaps(metadata, map[string]any{"_provenance": provenanceMap(SourceModelAssumption, nil)}),
		IsRoot:        true,
		ManualStatus:  core.Status(in.Confidence),
		AssertionKind: core.AssertionKindEmpirical,
	}
	if err := k.engine.AssertFact(fact); err != nil {
		return Node{}, err
	}
	node := Node{
		ID: id, Kind: KindAssumption, Text: in.Text, Source: SourceModelAssumption,
		Verification: Unverified, Epistemic: "explicit_assumption",
		Confidence: in.Confidence, Status: in.Confidence, Active: true,
		Metadata: cloneMap(metadata), CreatedAt: time.Now().UnixMilli(),
	}
	k.registerNode(node)
	return node, nil
}

func (k *Kernel) SetVerification(nodeID string, verification Verification, method string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	node, ok := k.nodes[nodeID]
	if !ok {
		return errors.New("node not found")
	}
	status := core.VerificationVerified
	if verification == Unverified {
		status = core.VerificationUnverified
	}
	if verification == Rejected {
		status = core.VerificationRejected
	}
	if err := k.engine.SetFactVerification(nodeID, status, method, "", "", 0); err != nil {
		return err
	}
	node.Verification = verification
	if node.Metadata == nil {
		node.Metadata = map[string]any{}
	}
	node.Metadata["verification_method"] = method
	k.nodes[nodeID] = node
	return nil
}

func (k *Kernel) MarkContradiction(claimID, assumptionID, evidenceID, reason string) (*ContradictionResult, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	claim, ok := k.nodes[claimID]
	if !ok {
		return nil, errors.New("claim not found")
	}
	guardID := claim.GuardID
	invalidated := []string{claimID}
	retracted := []string{}
	if assumptionID != "" {
		if err := k.engine.RetractFact(assumptionID, "verification failed: "+reason); err != nil {
			return nil, err
		}
		_ = k.engine.SetFactVerification(assumptionID, core.VerificationRejected, "verification", "", reason, 0)
		if node, ok := k.nodes[assumptionID]; ok {
			node.Active = false
			node.Status = 0
			node.Verification = Rejected
			k.nodes[assumptionID] = node
		}
		retracted = append(retracted, assumptionID)
		k.recordRetractionLocked(assumptionID, "verification failed: "+reason, "verification", true)
	}
	if guardID != "" {
		if err := k.engine.RetractFact(guardID, "contradiction: "+reason); err != nil {
			return nil, err
		}
		if guard, ok := k.nodes[guardID]; ok {
			guard.Active = false
			guard.Status = 0
			k.nodes[guardID] = guard
		}
	} else if err := k.engine.RetractFact(claimID, "contradicted: "+reason); err != nil {
		return nil, err
	}
	_ = k.engine.SetFactVerification(claimID, core.VerificationRejected, "contradiction", "", reason, 0)
	claim.Active = false
	claim.Status = float64(k.engine.GetStatus(claimID))
	claim.Verification = Rejected
	k.nodes[claimID] = claim
	k.recordRetractionLocked(claimID, reason, "contradiction", true)

	id := k.nextID("ctr")
	contradiction := Contradiction{
		ID: id, ClaimID: claimID, AssumptionID: assumptionID,
		EvidenceID: evidenceID, Reason: reason, At: time.Now().UnixMilli(),
	}
	k.contradictions = append(k.contradictions, contradiction)

	payload := k.factPayload(KindContradiction, reason, map[string]any{
		"claim_id": claimID, "assumption_id": assumptionID, "evidence_id": evidenceID,
	})
	ctrFact := &core.Fact{
		ID: id, Payload: payload,
		Metadata: map[string]any{
			"kind": string(KindContradiction), "source_type": string(SourceVerification),
			"verification_status": string(Verified),
		},
		IsRoot: true, ManualStatus: core.Valid, AssertionKind: core.AssertionKindEmpirical,
	}
	if err := k.engine.AssertFact(ctrFact); err != nil {
		return nil, err
	}
	k.registerNode(Node{
		ID: id, Kind: KindContradiction, Text: reason, Source: SourceVerification,
		Verification: Verified, Epistemic: "contradiction", Confidence: 1, Status: 1, Active: true,
		Metadata: cloneMap(payload["metadata"].(map[string]any)), CreatedAt: time.Now().UnixMilli(),
	})
	return &ContradictionResult{Contradiction: contradiction, Invalidated: invalidated, Retracted: retracted}, nil
}

func (k *Kernel) Retract(nodeID, reason, cause string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.engine.RetractFact(nodeID, reason); err != nil {
		return err
	}
	if node, ok := k.nodes[nodeID]; ok {
		node.Active = false
		node.Status = 0
		k.nodes[nodeID] = node
	}
	k.recordRetractionLocked(nodeID, reason, cause, cause != "manual_override")
	return nil
}

func (k *Kernel) recordRetractionLocked(nodeID, reason, cause string, correct bool) {
	for _, existing := range k.retractions {
		if existing.NodeID == nodeID && existing.Reason == reason {
			return
		}
	}
	k.retractions = append(k.retractions, Retraction{
		ID:     fmt.Sprintf("ret-%04d", len(k.retractions)+1),
		NodeID: nodeID, Reason: reason, Cause: cause, Correct: correct,
		At: time.Now().UnixMilli(),
	})
}

func (k *Kernel) RecordVerification(planID, claimID, assumptionID, evidenceID, method string, contradicted bool, reason string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.verifications = append(k.verifications, VerificationRecord{
		ID:     fmt.Sprintf("ver-%04d", len(k.verifications)+1),
		PlanID: planID, ClaimID: claimID, AssumptionID: assumptionID,
		EvidenceID: evidenceID, Method: method, Contradicted: contradicted,
		Correct: true, Reason: reason, At: time.Now().UnixMilli(),
	})
}

func (k *Kernel) Node(id string) (Node, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	node, ok := k.nodes[id]
	if !ok {
		return Node{}, false
	}
	if fact, exists := k.engine.GetFact(id); exists {
		node.Status = float64(k.engine.GetStatus(id))
		node.Active = node.Status >= 0.6
		node.Verification = verificationFromFact(fact, node.Verification)
	}
	return node, ok
}

func (k *Kernel) Nodes() []Node {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]Node, 0, len(k.nodes))
	for _, node := range k.nodes {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (k *Kernel) CheckConsistency() *core.ConsistencyReport {
	ids := []string{}
	for _, node := range k.Nodes() {
		if node.Active && node.Kind != KindContradiction {
			ids = append(ids, node.ID)
		}
	}
	return k.engine.CheckConsistency(ids, false)
}

func (k *Kernel) Snapshot() Snapshot {
	k.mu.Lock()
	nodes := make([]Node, 0, len(k.nodes))
	for _, node := range k.nodes {
		if fact, ok := k.engine.GetFact(node.ID); ok {
			node.Status = float64(k.engine.GetStatus(node.ID))
			node.Active = node.Status >= 0.6
			node.Verification = verificationFromFact(fact, node.Verification)
		}
		nodes = append(nodes, node)
	}
	contradictions := append([]Contradiction(nil), k.contradictions...)
	retractions := append([]Retraction(nil), k.retractions...)
	verifications := append([]VerificationRecord(nil), k.verifications...)
	k.mu.Unlock()

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	edges := k.edges()
	snapshot := Snapshot{
		RunID: k.runID, Nodes: nodes, Edges: edges,
		Contradictions: contradictions, Retractions: retractions,
		Verifications: verifications, GeneratedAt: time.Now().UnixMilli(),
	}
	snapshot.Stats = computeStats(nodes, contradictions, retractions, verifications)
	return snapshot
}

func verificationFromFact(fact *core.Fact, fallback Verification) Verification {
	if fact == nil || fact.Metadata == nil {
		return fallback
	}
	raw, _ := fact.Metadata["verification_status"].(string)
	switch raw {
	case "verified":
		return Verified
	case "rejected":
		return Rejected
	case "unverified":
		return Unverified
	default:
		return fallback
	}
}

func (k *Kernel) edges() []Edge {
	edges := []Edge{}
	for _, fact := range k.engine.ListFacts() {
		for setIndex, set := range fact.JustificationSets {
			for _, token := range set {
				negated := strings.HasPrefix(token, "!")
				from := strings.TrimPrefix(token, "!")
				edges = append(edges, Edge{From: from, To: fact.ID, Negated: negated, JustificationSet: setIndex})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].To == edges[j].To {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return edges
}

func computeStats(nodes []Node, contradictions []Contradiction, retractions []Retraction, verifications []VerificationRecord) Stats {
	stats := Stats{
		Nodes: len(nodes), Contradictions: len(contradictions),
		Retractions: len(retractions), Verifications: len(verifications),
	}
	verifiedAssumptions := map[string]bool{}
	byID := map[string]Node{}
	for _, node := range nodes {
		if node.Internal {
			stats.Nodes--
			continue
		}
		byID[node.ID] = node
		if node.Kind == KindAssumption && node.Verification == Verified {
			verifiedAssumptions[node.ID] = true
		}
	}
	memo := map[string]bool{}
	visiting := map[string]bool{}
	var supported func(Node) bool
	supported = func(node Node) bool {
		if value, ok := memo[node.ID]; ok {
			return value
		}
		if visiting[node.ID] {
			return false
		}
		visiting[node.ID] = true
		defer delete(visiting, node.ID)
		result := false
		switch node.Kind {
		case KindObservation:
			result = node.Active
		case KindClaim:
			if node.Active && len(node.EvidenceIDs) > 0 {
				result = true
				for _, evidenceID := range node.EvidenceIDs {
					parent, ok := byID[evidenceID]
					if !ok || !supported(parent) {
						result = false
						break
					}
				}
				if result {
					for _, assumptionID := range node.AssumptionIDs {
						if !verifiedAssumptions[assumptionID] {
							result = false
							break
						}
					}
				}
			}
		}
		memo[node.ID] = result
		return result
	}
	for _, node := range nodes {
		switch node.Kind {
		case KindObservation:
			stats.Observations++
		case KindAssumption:
			stats.Assumptions++
		case KindClaim:
			stats.Claims++
			if node.Active {
				stats.ActiveClaims++
				if !supported(node) {
					stats.UnsupportedClaims++
				}
			} else {
				stats.InvalidClaims++
			}
		}
	}
	if stats.ActiveClaims > 0 {
		stats.UnsupportedRate = float64(stats.UnsupportedClaims) / float64(stats.ActiveClaims)
	}
	for _, retraction := range retractions {
		if !retraction.Correct {
			stats.IncorrectRetractions++
		}
	}
	return stats
}

func (k *Kernel) Explain(factID string) (*core.ExplanationOutput, error) {
	return k.engine.ExplainReasoning(factID)
}

// ExportJSON returns a stable machine-readable evidence snapshot.
func (k *Kernel) ExportJSON() ([]byte, error) {
	return json.MarshalIndent(k.Snapshot(), "", "  ")
}
