package herdr

import (
	_ "embed"
	"encoding/json"
	"slices"
	"testing"
)

//go:embed schema.json
var schemaJSON []byte

type schemaDoc struct {
	Protocol int                        `json:"protocol"`
	Schemas  map[string]json.RawMessage `json:"schemas"`
}

type schemaNode struct {
	Const string                 `json:"const"`
	Enum  []string               `json:"enum"`
	Props map[string]*schemaNode `json:"properties"`
	OneOf []*schemaNode          `json:"oneOf"`
	Defs  map[string]*schemaNode `json:"$defs"`
}

// UnmarshalJSON tolerates boolean schemas (e.g. "explain": true).
func (n *schemaNode) UnmarshalJSON(b []byte) error {
	if string(b) == "true" || string(b) == "false" {
		return nil
	}
	type plain schemaNode
	return json.Unmarshal(b, (*plain)(n))
}

func loadSchema(t *testing.T) (schemaDoc, map[string]*schemaNode) {
	t.Helper()
	var doc schemaDoc
	if err := json.Unmarshal(schemaJSON, &doc); err != nil {
		t.Fatalf("parse schema.json: %v", err)
	}
	nodes := map[string]*schemaNode{}
	for name, raw := range doc.Schemas {
		var n schemaNode
		if err := json.Unmarshal(raw, &n); err != nil {
			t.Fatalf("parse schema %q: %v", name, err)
		}
		nodes[name] = &n
	}
	return doc, nodes
}

func def(t *testing.T, nodes map[string]*schemaNode, schema, name string) *schemaNode {
	t.Helper()
	s, ok := nodes[schema]
	if !ok {
		t.Fatalf("schema %q missing", schema)
	}
	d, ok := s.Defs[name]
	if !ok {
		t.Fatalf("schema %q has no $defs/%s", schema, name)
	}
	return d
}

// oneOfConsts collects properties[prop].const across a oneOf node.
func oneOfConsts(n *schemaNode, prop string) []string {
	var out []string
	for _, o := range n.OneOf {
		if p, ok := o.Props[prop]; ok && p.Const != "" {
			out = append(out, p.Const)
		}
	}
	return out
}

func requireAll(t *testing.T, what string, have []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(have, w) {
			t.Errorf("%s: %q not in schema (schema has %d entries)", what, w, len(have))
		}
	}
}

func TestSchemaProtocol(t *testing.T) {
	doc, _ := loadSchema(t)
	if doc.Protocol != 22 {
		t.Errorf("schema protocol = %d, want 22", doc.Protocol)
	}
	if ProtocolVersion != doc.Protocol {
		t.Errorf("ProtocolVersion = %d, schema protocol = %d", ProtocolVersion, doc.Protocol)
	}
}

func TestSchemaMethods(t *testing.T) {
	_, nodes := loadSchema(t)
	methods := oneOfConsts(nodes["request"], "method")
	if len(methods) == 0 {
		t.Fatal("no methods found in request schema")
	}
	requireAll(t, "method", methods,
		MethodSessionSnapshot, MethodEventsSubscribe, MethodAgentList, MethodAgentGet, MethodPaneRead)
}

func TestSchemaSubscriptionTypes(t *testing.T) {
	_, nodes := loadSchema(t)
	types := oneOfConsts(def(t, nodes, "request", "Subscription"), "type")
	requireAll(t, "subscription type", types,
		SubPaneCreated, SubPaneClosed, SubPaneUpdated, SubPaneMoved, SubPaneExited,
		SubPaneAgentDetected, SubPaneAgentStatusChanged)
}

func TestSchemaEventKinds(t *testing.T) {
	_, nodes := loadSchema(t)
	kinds := def(t, nodes, "event", "EventKind").Enum
	if len(kinds) == 0 {
		t.Fatal("EventKind enum empty")
	}
	requireAll(t, "event kind", kinds,
		EventPaneCreated, EventPaneClosed, EventPaneUpdated, EventPaneMoved, EventPaneExited,
		EventPaneAgentDetected, EventPaneAgentStatusChanged)

	// Each event kind must also have a matching EventData variant tagged with it.
	variants := oneOfConsts(def(t, nodes, "event", "EventData"), "type")
	requireAll(t, "event data variant", variants,
		EventPaneCreated, EventPaneClosed, EventPaneUpdated, EventPaneMoved, EventPaneExited,
		EventPaneAgentDetected, EventPaneAgentStatusChanged)
}

func TestSchemaSubscriptionEventKinds(t *testing.T) {
	_, nodes := loadSchema(t)
	kinds := def(t, nodes, "subscription_event", "SubscriptionEventKind").Enum
	requireAll(t, "subscription event kind", kinds, SubEventPaneAgentStatusChanged)
}

func TestSchemaResultTypes(t *testing.T) {
	_, nodes := loadSchema(t)
	results := oneOfConsts(def(t, nodes, "success_response", "ResponseResult"), "type")
	requireAll(t, "result type", results,
		ResultSessionSnapshot, ResultAgentList, ResultAgentInfo, ResultPaneRead, ResultSubscriptionStarted)
}

func TestSchemaAgentStatus(t *testing.T) {
	_, nodes := loadSchema(t)
	consts := []string{
		string(AgentStatusIdle), string(AgentStatusWorking), string(AgentStatusBlocked),
		string(AgentStatusDone), string(AgentStatusUnknown),
	}
	for _, schema := range []string{"event", "request", "success_response", "subscription_event"} {
		enum := def(t, nodes, schema, "AgentStatus").Enum
		if len(enum) == 0 {
			t.Fatalf("%s AgentStatus enum empty", schema)
		}
		requireAll(t, schema+" AgentStatus", enum, consts...)
		// Every schema value must have a constant, so new statuses are noticed.
		for _, v := range enum {
			if !slices.Contains(consts, v) {
				t.Errorf("%s AgentStatus enum value %q has no constant", schema, v)
			}
		}
	}
}

func TestSchemaReadEnums(t *testing.T) {
	_, nodes := loadSchema(t)
	requireAll(t, "ReadSource", def(t, nodes, "request", "ReadSource").Enum,
		string(ReadSourceVisible), string(ReadSourceRecent), string(ReadSourceRecentUnwrapped), string(ReadSourceDetection))
	requireAll(t, "ReadFormat", def(t, nodes, "request", "ReadFormat").Enum,
		string(ReadFormatText), string(ReadFormatANSI))
}

// TestSchemaDecodeIgnoresUnknownFields guards the plain-json decoding contract.
func TestSchemaDecodeIgnoresUnknownFields(t *testing.T) {
	in := `{"type":"pane_created","pane":{"pane_id":"p1","terminal_id":"t1","workspace_id":"w1","tab_id":"tab1","focused":true,"agent_status":"working","revision":3,"future_field":{"x":1}},"another_future":1}`
	var ev PaneCreatedEvent
	if err := json.Unmarshal([]byte(in), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Pane.TerminalID != "t1" || ev.Pane.AgentStatus != AgentStatusWorking {
		t.Errorf("unexpected decode: %+v", ev.Pane)
	}
}
