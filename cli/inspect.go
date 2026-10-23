package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/runstore"
)

func traceCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	runsDir := fs.String("runs-dir", defaultRunsDir(), "directory containing run artifacts")
	jsonOutput := fs.Bool("json", false, "print the event stream as JSON")
	eventTypes := fs.String("type", "", "comma-separated event types to include")
	tail := fs.Int("tail", 0, "show only the last N events (0 means all)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: paradox trace [flags] <run-id>")
		return 2
	}
	store, err := runstore.New(*runsDir)
	if err != nil {
		fmt.Fprintf(stderr, "trace: %v\n", err)
		return 1
	}
	events, err := store.ReadEvents(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "trace: %v\n", err)
		return 1
	}
	filter := map[string]bool{}
	for _, value := range strings.Split(*eventTypes, ",") {
		if value = strings.TrimSpace(value); value != "" {
			filter[value] = true
		}
	}
	filtered := make([]harness.Event, 0, len(events))
	for _, event := range events {
		if len(filter) > 0 && !filter[event.Type] {
			continue
		}
		filtered = append(filtered, event)
	}
	if *tail > 0 && len(filtered) > *tail {
		filtered = filtered[len(filtered)-*tail:]
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(filtered); err != nil {
			fmt.Fprintf(stderr, "trace: %v\n", err)
			return 1
		}
		return 0
	}
	for _, event := range filtered {
		step := ""
		if event.Step > 0 {
			step = fmt.Sprintf(" step=%d", event.Step)
		}
		fmt.Fprintf(stdout, "#%04d %s%s  %-24s %s\n",
			event.Seq, event.Time.Local().Format("15:04:05.000"), step,
			event.Type, eventSummary(event))
	}
	return 0
}

func eventSummary(event harness.Event) string {
	data := event.Data
	switch event.Type {
	case "run_started":
		return fmt.Sprintf("task=%s mode=%s provider=%s model=%s",
			dataString(data, "task_id"), dataString(data, "mode"),
			dataString(data, "provider"), dataString(data, "model"))
	case "provider_request":
		return fmt.Sprintf("model=%s messages=%d tools=%d max_tokens=%d",
			dataString(data, "model"), dataInt(data, "messages"), dataInt(data, "tools"), dataInt(data, "max_tokens"))
	case "provider_response":
		content := truncateText(dataString(data, "content"), 180)
		if content == "" {
			content = fmt.Sprintf("tool_calls=%d", len(dataSlice(data, "tool_calls")))
		}
		usage := dataMap(data, "usage")
		return fmt.Sprintf("%s (tokens=%d finish=%s)", content, dataInt(usage, "total_tokens"), dataString(data, "finish_reason"))
	case "provider_error":
		return fmt.Sprintf("attempt=%d %s", dataInt(data, "attempt"), dataString(data, "error"))
	case "assistant_message":
		content := truncateText(dataString(data, "content"), 180)
		calls := dataSlice(data, "tool_calls")
		if content == "" && len(calls) > 0 {
			names := make([]string, 0, len(calls))
			for _, call := range calls {
				if mapped, ok := call.(map[string]any); ok {
					names = append(names, dataString(mapped, "name"))
				}
			}
			return "tool_calls=" + strings.Join(names, ",")
		}
		if len(calls) > 0 {
			return fmt.Sprintf("%s tool_calls=%d", content, len(calls))
		}
		return content
	case "tool_call":
		return fmt.Sprintf("%s origin=%s args=%s", dataString(data, "tool"), dataString(data, "origin"), truncateText(dataString(data, "arguments"), 180))
	case "tool_result":
		return fmt.Sprintf("%s ok=%t evidence=%s", dataString(data, "tool"), dataBool(data, "ok"), strings.Join(dataStrings(data, "evidence_ids"), ","))
	case "observation":
		node := dataMap(data, "node")
		return fmt.Sprintf("%s %s", dataString(node, "id"), truncateText(dataString(node, "text"), 200))
	case "claim_submitted":
		claim := dataMap(data, "claim")
		return fmt.Sprintf("%s %s (guard=%s)", dataString(claim, "id"), truncateText(dataString(claim, "text"), 200), dataString(data, "guard_id"))
	case "assumption_recorded":
		node := dataMap(data, "node")
		return fmt.Sprintf("%s %s", dataString(node, "id"), truncateText(dataString(node, "text"), 200))
	case "verification_planned":
		plan := dataMap(data, "plan")
		return fmt.Sprintf("%s claim=%s %s", dataString(plan, "id"), dataString(data, "claim_id"), truncateText(dataString(plan, "rationale"), 240))
	case "verification_passed":
		return fmt.Sprintf("claim=%s assumption=%s %s", dataString(data, "claim_id"), dataString(data, "assumption_id"), truncateText(dataString(data, "reason"), 200))
	case "contradiction_detected":
		contradiction := dataMap(data, "contradiction")
		return fmt.Sprintf("claim=%s evidence=%s %s", dataString(contradiction, "claim_id"), dataString(contradiction, "evidence_id"), truncateText(dataString(contradiction, "reason"), 240))
	case "claim_invalidated":
		return fmt.Sprintf("claim=%s reason=%s", dataString(data, "claim_id"), truncateText(dataString(data, "reason"), 240))
	case "replan_required":
		return truncateText(dataString(data, "reason"), 240)
	case "consistency_check":
		return fmt.Sprintf("issues=%d", dataInt(data, "issue_count"))
	case "budget_exhausted":
		return fmt.Sprintf("kind=%s used=%v limit=%v", dataString(data, "kind"), data["used"], data["limit"])
	case "run_finished":
		return fmt.Sprintf("success=%t score=%v error=%s", dataBool(data, "success"), data["score"], truncateText(dataString(data, "error"), 160))
	case "agent_finished":
		return truncateText(dataString(data, "conclusion"), 240)
	case "error":
		return truncateText(dataString(data, "error"), 300)
	case "persistence_error":
		return truncateText(dataString(data, "error"), 300)
	case "cost_unavailable":
		return truncateText(dataString(data, "message"), 200)
	default:
		encoded, _ := json.Marshal(data)
		return truncateText(string(encoded), 240)
	}
}

func evidenceCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evidence", flag.ContinueOnError)
	fs.SetOutput(stderr)
	runsDir := fs.String("runs-dir", defaultRunsDir(), "directory containing run artifacts")
	jsonOutput := fs.Bool("json", false, "print the evidence snapshot as JSON")
	nodeID := fs.String("node", "", "focus on one node and its direct relations")
	showEdges := fs.Bool("edges", false, "print the dependency graph edges")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: paradox evidence [flags] <run-id>")
		return 2
	}
	store, err := runstore.New(*runsDir)
	if err != nil {
		fmt.Fprintf(stderr, "evidence: %v\n", err)
		return 1
	}
	record, err := store.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "evidence: %v\n", err)
		return 1
	}
	if record.Evidence == nil {
		fmt.Fprintln(stderr, "evidence: run has no persisted evidence snapshot (it may still be running or may have failed before persistence)")
		return 1
	}
	snapshot := *record.Evidence
	if *jsonOutput {
		var value any = snapshot
		if *nodeID != "" {
			value = focusNode(snapshot, *nodeID)
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(value); err != nil {
			fmt.Fprintf(stderr, "evidence: %v\n", err)
			return 1
		}
		return 0
	}
	if *nodeID != "" {
		focus := focusNode(snapshot, *nodeID)
		if focus.Node == nil {
			fmt.Fprintf(stderr, "evidence: node %q not found\n", *nodeID)
			return 1
		}
		printNode(stdout, *focus.Node)
		if len(focus.Edges) > 0 {
			fmt.Fprintln(stdout, "  relations:")
			for _, edge := range focus.Edges {
				fmt.Fprintf(stdout, "    %s -> %s negated=%t set=%d\n", edge.From, edge.To, edge.Negated, edge.JustificationSet)
			}
		}
		return 0
	}

	stats := snapshot.Stats
	fmt.Fprintf(stdout, "Evidence for run %s\n", snapshot.RunID)
	fmt.Fprintf(stdout, "  nodes=%d observations=%d assumptions=%d claims=%d active=%d unsupported=%d invalid=%d\n",
		stats.Nodes, stats.Observations, stats.Assumptions, stats.Claims,
		stats.ActiveClaims, stats.UnsupportedClaims, stats.InvalidClaims)
	fmt.Fprintf(stdout, "  contradictions=%d retractions=%d verifications=%d unsupported_rate=%.3f incorrect_retractions=%d\n",
		stats.Contradictions, stats.Retractions, stats.Verifications, stats.UnsupportedRate, stats.IncorrectRetractions)

	printNodesByKind(stdout, "Claims", snapshot.Nodes, evidence.KindClaim)
	printNodesByKind(stdout, "Observations", snapshot.Nodes, evidence.KindObservation)
	printNodesByKind(stdout, "Assumptions", snapshot.Nodes, evidence.KindAssumption)
	printNodesByKind(stdout, "Contradictions", snapshot.Nodes, evidence.KindContradiction)

	if len(snapshot.Retractions) > 0 {
		fmt.Fprintln(stdout, "Retractions:")
		for _, retraction := range snapshot.Retractions {
			fmt.Fprintf(stdout, "  %s node=%s cause=%s correct=%t %s\n",
				retraction.ID, retraction.NodeID, retraction.Cause, retraction.Correct, retraction.Reason)
		}
	}
	if len(snapshot.Verifications) > 0 {
		fmt.Fprintln(stdout, "Verifications:")
		for _, verification := range snapshot.Verifications {
			fmt.Fprintf(stdout, "  %s plan=%s claim=%s assumption=%s method=%s contradicted=%t %s\n",
				verification.ID, verification.PlanID, verification.ClaimID, verification.AssumptionID,
				verification.Method, verification.Contradicted, verification.Reason)
		}
	}
	if *showEdges {
		fmt.Fprintln(stdout, "Edges:")
		for _, edge := range snapshot.Edges {
			fmt.Fprintf(stdout, "  %s -> %s negated=%t set=%d\n", edge.From, edge.To, edge.Negated, edge.JustificationSet)
		}
	}
	return 0
}

type focusedNode struct {
	Node  *evidence.Node  `json:"node"`
	Edges []evidence.Edge `json:"edges"`
}

func focusNode(snapshot evidence.Snapshot, nodeID string) focusedNode {
	focus := focusedNode{}
	for index := range snapshot.Nodes {
		if snapshot.Nodes[index].ID == nodeID {
			node := snapshot.Nodes[index]
			focus.Node = &node
			break
		}
	}
	for _, edge := range snapshot.Edges {
		if edge.From == nodeID || edge.To == nodeID {
			focus.Edges = append(focus.Edges, edge)
		}
	}
	return focus
}

func printNodesByKind(w io.Writer, title string, nodes []evidence.Node, kind evidence.Kind) {
	selected := []evidence.Node{}
	for _, node := range nodes {
		if node.Kind == kind && !node.Internal {
			selected = append(selected, node)
		}
	}
	if len(selected) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", title)
	for _, node := range selected {
		printNode(w, node)
	}
}

func printNode(w io.Writer, node evidence.Node) {
	state := "INVALID"
	if node.Active {
		state = "ACTIVE"
	}
	fmt.Fprintf(w, "  [%s] %s %s status=%.2f verification=%s\n", state, node.ID, node.Kind, node.Status, node.Verification)
	fmt.Fprintf(w, "        %s\n", truncateText(node.Text, 500))
	if node.ClaimKey != "" || node.ClaimValue != "" {
		fmt.Fprintf(w, "        claim_key=%s value=%s\n", node.ClaimKey, node.ClaimValue)
	}
	if len(node.EvidenceIDs) > 0 {
		fmt.Fprintf(w, "        evidence=%s\n", strings.Join(node.EvidenceIDs, ","))
	}
	if len(node.AssumptionIDs) > 0 {
		fmt.Fprintf(w, "        assumptions=%s\n", strings.Join(node.AssumptionIDs, ","))
	}
	if len(node.Artifacts) > 0 {
		names := make([]string, 0, len(node.Artifacts))
		for _, artifact := range node.Artifacts {
			names = append(names, artifact.Name)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "        artifacts=%s\n", strings.Join(names, ","))
	}
}

func dataMap(data map[string]any, key string) map[string]any {
	value, _ := data[key].(map[string]any)
	return value
}

func dataString(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

func dataStrings(data map[string]any, key string) []string {
	values, _ := data[key].([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func dataSlice(data map[string]any, key string) []any {
	values, _ := data[key].([]any)
	return values
}

func dataInt(data map[string]any, key string) int {
	switch value := data[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func dataBool(data map[string]any, key string) bool {
	value, _ := data[key].(bool)
	return value
}
