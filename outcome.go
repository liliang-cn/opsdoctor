package opspilot

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Outcome is how a turn ended: the agent finished the task, or it stopped
// because something only the operator can resolve stood in the way. Text is
// the agent's own summary or blocker, empty when the answer already says it.
type Outcome struct {
	Status string `json:"status"` // "complete" | "blocked"
	Text   string `json:"text,omitempty"`
}

// outcomeFromCall reads a turn-ending tool call; nil for any other tool.
func outcomeFromCall(tool string, args map[string]any) *Outcome {
	switch tool {
	case "task_complete":
		s, _ := args["result"].(string)
		return &Outcome{Status: "complete", Text: strings.TrimSpace(s)}
	case "task_blocked":
		s, _ := args["blocker"].(string)
		return &Outcome{Status: "blocked", Text: strings.TrimSpace(s)}
	}
	return nil
}

// writtenOutcome matches a turn-ending call a model wrote at the end of its
// answer instead of making it, in the two shapes seen: JSON arguments
// (task_complete{"result": "..."}) and call syntax (task_blocked(blocker="...")).
var writtenOutcome = regexp.MustCompile(`(?s)(?:^|\n|\s)(task_complete|task_blocked)\s*([\{\(].*[\}\)])\s*$`)

// namedArgPattern pulls a result= / blocker= value out of call syntax.
var namedArgPattern = regexp.MustCompile(`(?:result|blocker)\s*[=:]\s*"((?:[^"\\]|\\.)*)"`)

// callArgPattern is the fallback: the first quoted value.
var callArgPattern = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// splitWrittenOutcome separates such a written call from the answer before
// it. It returns the answer unchanged and nil when there is none.
func splitWrittenOutcome(answer string) (string, *Outcome) {
	m := writtenOutcome.FindStringSubmatchIndex(answer)
	if m == nil {
		return answer, nil
	}
	tool := answer[m[2]:m[3]]
	raw := answer[m[4]:m[5]]

	args := map[string]any{}
	// task_blocked({"blocker": ...}) is JSON wrapped in call parentheses.
	if inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "("), ")")); strings.HasPrefix(raw, "(") && strings.HasPrefix(inner, "{") {
		raw = inner
	}
	if strings.HasPrefix(raw, "{") && json.Unmarshal([]byte(raw), &args) != nil {
		return answer, nil // not a call after all
	}
	if strings.HasPrefix(raw, "(") {
		v := namedArgPattern.FindStringSubmatch(raw)
		if v == nil {
			v = callArgPattern.FindStringSubmatch(raw)
		}
		if v == nil {
			return answer, nil
		}
		text := v[1]
		if unq, err := unquote(`"` + v[1] + `"`); err == nil {
			text = unq
		}
		key := "result"
		if tool == "task_blocked" {
			key = "blocker"
		}
		args[key] = text
	}
	return strings.TrimSpace(answer[:m[0]]), outcomeFromCall(tool, args)
}

func unquote(s string) (string, error) {
	var out string
	err := json.Unmarshal([]byte(s), &out)
	return out, err
}
