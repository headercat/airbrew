// Package defn holds the workflow definition model: the JSON graph (nodes +
// edges) authored in the editor, plus the validation rules that gate save and
// activation. The engine and the HTTP layer import this package, so the
// domain stays free of database concerns.
package defn

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// TriggerType enumerates the trigger node kinds a workflow can start from.
type TriggerType string

const (
	TriggerManual   TriggerType = "manual"
	TriggerWebhook  TriggerType = "webhook"
	TriggerSchedule TriggerType = "schedule"
)

// NodeType prefixes its category; the suffix is the specific behaviour.
const (
	prefixTrigger = "trigger."
	prefixLogic   = "logic."
	prefixAction  = "action."
)

// Definition is the persisted graph: a list of nodes and the edges wiring
// them. It is stored as JSON in workflows.definition.
type Definition struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Node is one step in the graph. The runtime dispatches on Type and reads
// Config as the per-type parameters.
type Node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Edge wires one node's output port to another node's input. The default
// port is "out"; logic.if emits "true" / "false" instead.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Port string `json:"port,omitempty"`
}

// Marshal serialises a Definition to JSON.
func (d Definition) Marshal() (string, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("workflow: marshal definition: %w", err)
	}
	return string(b), nil
}

// UnmarshalDefinition parses raw JSON into a Definition.
func UnmarshalDefinition(raw string) (Definition, error) {
	var d Definition
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Definition{Nodes: []Node{}, Edges: []Edge{}}, nil
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return Definition{}, fmt.Errorf("workflow: parse definition: %w", err)
	}
	if d.Nodes == nil {
		d.Nodes = []Node{}
	}
	if d.Edges == nil {
		d.Edges = []Edge{}
	}
	return d, nil
}

// IsTrigger reports whether a node type is a trigger.
func IsTrigger(t string) bool { return strings.HasPrefix(t, prefixTrigger) }

// IsLogic reports whether a node type is a logic node.
func IsLogic(t string) bool { return strings.HasPrefix(t, prefixLogic) }

// IsAction reports whether a node type is an action node.
func IsAction(t string) bool { return strings.HasPrefix(t, prefixAction) }

// TriggerTypeOf returns the TriggerType for the trigger node, or "" if the
// node is not a trigger.
func TriggerTypeOf(t string) TriggerType {
	switch t {
	case "trigger.manual":
		return TriggerManual
	case "trigger.webhook":
		return TriggerWebhook
	case "trigger.schedule":
		return TriggerSchedule
	}
	return ""
}

// allowedNodeTypes is the catalog of recognised node types. Anything outside
// this set is rejected at validation time so unknown types cannot silently
// no-op at runtime.
var allowedNodeTypes = map[string]bool{
	"trigger.manual":   true,
	"trigger.webhook":  true,
	"trigger.schedule": true,
	"logic.if":         true,
	"logic.delay":      true,
	"logic.setvar":     true,
	"action.http":      true,
	"action.mail":      true,
	"action.log":       true,
}

var nodeIDRe = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

// Validate checks the graph for structural problems. It returns nil if the
// graph is well-formed, otherwise a sentinel ValidationError wrapping the
// specific problem. The caller should map ValidationError to HTTP 400.
func (d Definition) Validate() error {
	if len(d.Nodes) == 0 {
		return ValidationErrorf("graph has no nodes")
	}
	ids := make(map[string]*Node, len(d.Nodes))
	triggerCount := 0
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if !nodeIDRe.MatchString(n.ID) {
			return ValidationErrorf("node id %q is invalid (use letters, digits, _ or -)", n.ID)
		}
		if _, dup := ids[n.ID]; dup {
			return ValidationErrorf("node id %q is duplicated", n.ID)
		}
		ids[n.ID] = n
		if !allowedNodeTypes[n.Type] {
			return ValidationErrorf("node %q has unknown type %q", n.ID, n.Type)
		}
		if IsTrigger(n.Type) {
			triggerCount++
		}
	}
	if triggerCount == 0 {
		return ValidationErrorf("graph has no trigger node")
	}
	if triggerCount > 1 {
		return ValidationErrorf("graph has multiple trigger nodes (%d); only one is allowed", triggerCount)
	}

	// Edges: endpoints exist, the trigger has no inbound, ports match the
	// source node type, and the graph is acyclic.
	hasInbound := make(map[string]bool, len(d.Nodes))
	for i, e := range d.Edges {
		if ids[e.From] == nil {
			return ValidationErrorf("edge %d starts from unknown node %q", i, e.From)
		}
		if ids[e.To] == nil {
			return ValidationErrorf("edge %d points to unknown node %q", i, e.To)
		}
		if e.From == e.To {
			return ValidationErrorf("edge %d is a self-loop on %q", i, e.From)
		}
		if port := normalisePort(e.Port); port != "" {
			if err := checkPort(ids[e.From].Type, port, i); err != nil {
				return err
			}
		}
		hasInbound[e.To] = true
	}
	// Trigger must be the sole root.
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if IsTrigger(n.Type) {
			if hasInbound[n.ID] {
				return ValidationErrorf("trigger node %q has incoming edges", n.ID)
			}
		} else if !hasInbound[n.ID] {
			return ValidationErrorf("node %q is unreachable (no incoming edges)", n.ID)
		}
	}
	if err := d.checkAcyclic(ids); err != nil {
		return err
	}
	return nil
}

// normalisePort maps the empty/zero value to "out" and lower-cases the rest.
func normalisePort(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return "out"
	}
	return p
}

// checkPort validates that port is a legal output of the source node type.
func checkPort(fromType, port string, edgeIdx int) error {
	switch {
	case fromType == "logic.if":
		if port != "true" && port != "false" {
			return ValidationErrorf("edge %d: logic.if has no port %q (want true|false)", edgeIdx, port)
		}
	default:
		if port != "out" {
			return ValidationErrorf("edge %d: node type %q has no port %q (want out)", edgeIdx, fromType, port)
		}
	}
	return nil
}

// checkAcyclic runs a DFS and rejects back edges. Cycles in a workflow graph
// would either loop forever or rely on side-effects to terminate — neither
// is a behaviour we want to support in v1.
func (d Definition) checkAcyclic(nodes map[string]*Node) error {
	// Build adjacency.
	adj := make(map[string][]string, len(d.Edges))
	for _, e := range d.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(nodes))
	var dfs func(string) error
	dfs = func(id string) error {
		switch color[id] {
		case gray:
			return ValidationErrorf("graph contains a cycle through %q", id)
		case black:
			return nil
		}
		color[id] = gray
		for _, next := range adj[id] {
			if err := dfs(next); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}
	// DFS from every node so we cover disconnected components too.
	for i := range d.Nodes {
		if err := dfs(d.Nodes[i].ID); err != nil {
			return err
		}
	}
	return nil
}

// TriggerNode returns the graph's trigger node. The caller is expected to
// have validated the definition first (which guarantees exactly one
// trigger).
func (d Definition) TriggerNode() *Node {
	for i := range d.Nodes {
		if IsTrigger(d.Nodes[i].Type) {
			return &d.Nodes[i]
		}
	}
	return nil
}

// NextNodes returns the nodes reachable from id via the given output port.
// The returned nodes retain the order they appear in the graph so the
// runtime walk is deterministic.
func (d Definition) NextNodes(id, port string) []*Node {
	port = normalisePort(port)
	if port == "" {
		port = "out"
	}
	targets := make(map[string]bool)
	for _, e := range d.Edges {
		if e.From == id && normalisePort(e.Port) == port {
			targets[e.To] = true
		}
	}
	if len(targets) == 0 {
		return nil
	}
	var out []*Node
	for i := range d.Nodes {
		if targets[d.Nodes[i].ID] {
			out = append(out, &d.Nodes[i])
		}
	}
	return out
}

// --- errors ----------------------------------------------------------------

// ValidationError is returned by Validate. It always carries a human-facing
// message; the HTTP layer maps it to 400.
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return e.msg }

// IsValidationError reports whether err is a *ValidationError.
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// ValidationErrorf builds a ValidationError with fmt formatting.
func ValidationErrorf(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}
