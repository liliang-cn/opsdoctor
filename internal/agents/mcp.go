// This file mounts an external MCP server's tools into the ops agent's ReAct
// loop. Only read-only tools are exposed when a spec is marked ReadOnly, so a
// consumer can safely wire a cluster-control MCP server (e.g. sds-mcp) and let
// the agent observe state without ever mutating it. Actual mutations are surfaced
// as structured, operator-approved suggestions (see registerSuggestAction).
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/mcp"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPSpec describes one external MCP server to mount. It mirrors the public
// opspilot.MCPServerSpec so internal packages never import the root facade.
type MCPSpec struct {
	Name              string
	Transport         string // "stdio" | "http" | "sse"
	Command           string
	Args              []string
	URL               string
	Headers           map[string]string
	ReadOnly          bool
	ReadOnlyToolAllow []string
	WriteToolAllow    []string // mutating tools mounted despite ReadOnly
}

// WriteGate decides whether one mutating tool call may run. It is consulted
// before the call reaches the MCP server; a call it refuses is not executed,
// and reason is what the agent is told instead of a result.
type WriteGate func(ctx context.Context, server, tool string, args map[string]interface{}) (approved bool, reason string)

// MCPMountStatus reports the outcome of mounting one MCP server.
type MCPMountStatus struct {
	Name      string // logical server name
	Connected bool   // true if Connect succeeded
	Tools     int    // number of tools actually registered into the agent
	Skipped   int    // tools filtered out by the read-only gate
	Err       string // connect/list error, empty on success
}

// mcpConnectTimeout bounds each server's initialize handshake so a dead server
// never blocks agent construction.
const mcpConnectTimeout = 30 * time.Second

// MountMCP connects each spec's MCP server and registers its (read-only) tools
// into svc. It never returns an error: a server that fails to connect is recorded
// in the returned status slice and skipped, so the agent degrades to knowledge-only.
// Callers must Close every returned client. A non-nil gate is consulted before
// every mutating tool call.
func MountMCP(ctx context.Context, svc *agent.Service, specs []MCPSpec, gate WriteGate) ([]*mcp.Client, []MCPMountStatus) {
	var clients []*mcp.Client
	var statuses []MCPMountStatus
	// One cache across every server: the key carries the tool name, and two
	// servers exposing the same tool name are already indistinguishable to the
	// model, so they should not be distinguished here either.
	cache := newToolCache()

	for _, spec := range specs {
		st := MCPMountStatus{Name: spec.Name}

		client, err := mcp.NewClient(serverConfig(spec), nil)
		if err != nil {
			st.Err = fmt.Sprintf("new client: %v", err)
			statuses = append(statuses, st)
			continue
		}

		cctx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
		err = client.Connect(cctx)
		cancel()
		if err != nil {
			st.Err = fmt.Sprintf("connect: %v", err)
			_ = client.Close()
			statuses = append(statuses, st)
			continue
		}
		st.Connected = true
		clients = append(clients, client)

		for name, tool := range client.GetTools() {
			if spec.ReadOnly && namedIn(name, spec.WriteToolAllow) {
				registerMCPTool(svc, client, spec.Name, name, tool, false, cache, gate)
				st.Tools++
				continue
			}
			if spec.ReadOnly && !readOnlyAdmitTool(name, tool, spec.ReadOnlyToolAllow) {
				st.Skipped++
				continue
			}
			// On a server mounted whole, only the tools that look mutating are
			// gated: asking an operator to approve a status read is how an
			// approval prompt becomes one people click through unread.
			var g WriteGate
			if !spec.ReadOnly && !readOnlyAdmitTool(name, tool, spec.ReadOnlyToolAllow) {
				g = gate
			}
			registerMCPTool(svc, client, spec.Name, name, tool, spec.ReadOnly, cache, g)
			st.Tools++
		}
		statuses = append(statuses, st)
	}

	return clients, statuses
}

func namedIn(name string, list []string) bool {
	for _, n := range list {
		if strings.EqualFold(strings.TrimSpace(n), name) {
			return true
		}
	}
	return false
}

// serverConfig translates a spec into an agent-go mcp.ServerConfig.
func serverConfig(spec MCPSpec) *mcp.ServerConfig {
	cfg := &mcp.ServerConfig{
		Name:           spec.Name,
		URL:            spec.URL,
		Headers:        spec.Headers,
		DefaultTimeout: mcpConnectTimeout,
	}
	switch strings.ToLower(strings.TrimSpace(spec.Transport)) {
	case "http":
		cfg.Type = mcp.ServerTypeHTTP
	case "sse":
		cfg.Type = mcp.ServerTypeSSE
	default: // "stdio" or empty
		cfg.Type = mcp.ServerTypeStdio
		if spec.Command != "" {
			cfg.Command = []string{spec.Command}
		}
		cfg.Args = spec.Args
	}
	return cfg
}

// registerMCPTool exposes one MCP tool as an agent tool. The handler forwards the
// call to the MCP server via CallTool; results and errors are returned as plain
// JSON-safe maps so the existing Stream event path surfaces them unchanged.
func registerMCPTool(svc *agent.Service, client *mcp.Client, server, name string, tool *sdkmcp.Tool, readOnly bool, cache *toolCache, gate WriteGate) {
	desc := tool.Description
	if desc == "" {
		desc = fmt.Sprintf("Tool %q exposed by MCP server %q.", name, server)
	}
	handler := mcpToolHandler(client, server, name, gate)
	// Flag read-only tools explicitly. agent-go's name heuristic doesn't treat
	// e.g. "*_status" as read-only, so without this a duplicate status poll would
	// be re-executed instead of collapsed. A ReadOnly tool is also concurrency-safe.
	if readOnly {
		// Collapsed here as well as flagged upstream: the metadata alone did not
		// stop a reasoning loop from calling the same list tool four times in one
		// turn, each a round trip and another copy of the result in the context.
		// Only read-only tools are wrapped — a cached mutation would report an
		// effect that did not happen the second time.
		svc.AddToolWithMetadata(name, desc, toParams(tool.InputSchema), cache.wrap(name, handler),
			agent.ToolMetadata{ReadOnly: true, ConcurrencySafe: true})
		return
	}
	svc.AddTool(name, desc, toParams(tool.InputSchema), handler)
}

// toolCaller is the part of an MCP client a tool handler uses.
type toolCaller interface {
	CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*mcp.ToolResult, error)
}

// mcpToolHandler forwards one tool to its MCP server, through gate when set: a
// call the gate refuses never reaches the server, and the agent gets the
// gate's reason in place of a result.
func mcpToolHandler(c toolCaller, server, call string, gate WriteGate) func(context.Context, map[string]interface{}) (interface{}, error) {
	return func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
		if gate != nil {
			if ok, reason := gate(ctx, server, call, args); !ok {
				return map[string]interface{}{"ok": false, "executed": false, "error": reason}, nil
			}
		}
		res, err := c.CallTool(ctx, call, args)
		if err != nil {
			return map[string]interface{}{"ok": false, "error": err.Error()}, nil
		}
		if res != nil && !res.Success {
			return map[string]interface{}{"ok": false, "error": res.Error}, nil
		}
		var data interface{}
		if res != nil {
			data = res.Data
		}
		return map[string]interface{}{"ok": true, "data": data}, nil
	}
}

// readOnlyAdmitTool decides whether a tool may be mounted under ReadOnly,
// preferring what the server DECLARED over what its name looks like.
//
// MCP publishes a readOnlyHint for exactly this question, and a server knows
// whether its own tool mutates anything — no name heuristic can beat that. The
// heuristic stays for the servers that publish nothing, which is most of them.
//
// Only a hint of TRUE is acted on. The wire format omits a false hint, so an
// unannotated tool and one explicitly marked "not read-only" arrive
// identically; treating that as a declaration would hand the heuristic's job to
// an absence. Falling through costs nothing, since the heuristic refuses
// mutating names anyway.
//
// The allowlist wins over both: it is the operator's override, and overriding a
// server's own annotation is a deliberate act.
func readOnlyAdmitTool(name string, tool *sdkmcp.Tool, allow []string) bool {
	if len(allow) > 0 {
		return readOnlyAdmit(name, allow)
	}
	if tool != nil && tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
		return true
	}
	return readOnlyAdmit(name, nil)
}

// readOnlyAdmit decides whether a tool may be mounted under ReadOnly.
//
//   - If an explicit allowlist is provided, ONLY names in it are admitted.
//   - Otherwise the name decides: the trailing segment is read as the operation,
//     and only if that is inconclusive is the whole name scanned, reject-first.
//     A name matching nothing is rejected (fail closed).
//
// MCP defines a readOnlyHint annotation for exactly this question, and servers
// do set it — but agent-go's tool wrapper exposes only Name, Description and
// Schema, so the annotation never reaches this package. The name is all there
// is. Prefer the allowlist when a server's naming cannot carry the meaning.
//
// Reading the TRAILING segment first is what makes the heuristic usable. These
// names are "<subject>_<operation>", so scanning the whole string let a subject
// veto its own operation: "sds_ha_promoter_status" was rejected because the
// subject is a promoter, and "sds_snapshot_schedule_list" because the subject is
// a schedule — both plainly read-only, both refused.
//
// Matching is per SEGMENT and exact, never substring. A substring scan admitted
// "sds_widget_frobnicate", because "widget" contains "get" — and the same trap
// waits in "address" for "add" and "gadget" for "get". For a gate that decides
// what an LLM may call, a name nobody can classify must be refused, not guessed
// at; the allowlist is the way through for a server whose naming carries no
// operation at all.
func readOnlyAdmit(name string, allow []string) bool {
	if len(allow) > 0 {
		for _, a := range allow {
			if strings.EqualFold(strings.TrimSpace(a), name) {
				return true
			}
		}
		return false
	}
	segs := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '_' || r == '.' || r == '-'
	})
	if len(segs) == 0 {
		return false
	}

	// The operation is the last segment; it settles the question by itself.
	switch op := segs[len(segs)-1]; {
	case slices.Contains(readOnlyAdmitTokens, op):
		return true
	case slices.Contains(readOnlyRejectTokens, op):
		return false
	}

	// It ended in a noun ("sds_pool_add_disk", "sds_gateway_create_nfs"), so the
	// operation is somewhere earlier. Rejection wins over admission.
	for _, seg := range segs {
		if slices.Contains(readOnlyRejectTokens, seg) {
			return false
		}
	}
	for _, seg := range segs {
		if slices.Contains(readOnlyAdmitTokens, seg) {
			return true
		}
	}
	return false
}

// trailingSegment returns the last "_"- or "."-delimited part of a tool name,
// which is where these naming schemes put the operation. Empty when the name
// has no separator to split on, in which case there is no operation to read
// apart from the name itself and the caller falls back to scanning it whole.
func trailingSegment(name string) string {
	i := strings.LastIndexAny(name, "_.")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return name[i+1:]
}

// readOnlyAdmitTokens name a tool as observational.
var readOnlyAdmitTokens = []string{
	"list", "get", "status", "health", "describe", "show", "metadata",
}

// readOnlyRejectTokens name a tool as mutating; presence forces rejection.
var readOnlyRejectTokens = []string{
	"create", "delete", "remove", "set", "evict", "promote", "demote",
	"start", "stop", "mount", "unmount", "resize", "add", "register",
	"unregister", "restore", "enable", "disable", "adopt", "schedule",
}

// toParams normalizes an MCP tool's JSON-schema input into the map[string]any
// shape agent-go's AddTool expects. It round-trips through JSON so it works
// whether the SDK hands us a map, a typed schema, or json.RawMessage.
func toParams(schema interface{}) map[string]interface{} {
	empty := map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	if schema == nil {
		return empty
	}
	if m, ok := schema.(map[string]interface{}); ok && len(m) > 0 {
		return m
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return empty
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil || len(m) == 0 {
		return empty
	}
	return m
}
