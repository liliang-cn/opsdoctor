package agents

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/mcp"
)

type recordingCaller struct{ calls []string }

func (r *recordingCaller) CallTool(_ context.Context, name string, _ map[string]interface{}) (*mcp.ToolResult, error) {
	r.calls = append(r.calls, name)
	return &mcp.ToolResult{Success: true, Data: "done"}, nil
}

// A call the gate refuses must never reach the server, and the agent must be
// told it was not executed rather than handed an empty success.
func TestGatedToolRefusedNeverReachesTheServer(t *testing.T) {
	c := &recordingCaller{}
	var asked []string
	gate := func(_ context.Context, server, tool string, args map[string]interface{}) (bool, string) {
		asked = append(asked, server+"/"+tool)
		return false, "not executed: declined"
	}
	out, err := mcpToolHandler(c, "sds", "sds_pool_create", gate)(context.Background(), map[string]interface{}{"name": "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 0 {
		t.Fatalf("refused call reached the server: %v", c.calls)
	}
	res := out.(map[string]interface{})
	if res["ok"] != false || res["executed"] != false || res["error"] != "not executed: declined" {
		t.Fatalf("agent was not told the call was refused: %v", res)
	}
	if len(asked) != 1 || asked[0] != "sds/sds_pool_create" {
		t.Fatalf("gate asked about %v", asked)
	}
}

func TestGatedToolApprovedRunsOnce(t *testing.T) {
	c := &recordingCaller{}
	gate := func(context.Context, string, string, map[string]interface{}) (bool, string) { return true, "" }
	out, _ := mcpToolHandler(c, "sds", "sds_pool_create", gate)(context.Background(), nil)
	if len(c.calls) != 1 || out.(map[string]interface{})["ok"] != true {
		t.Fatalf("approved call: calls=%v result=%v", c.calls, out)
	}
}
