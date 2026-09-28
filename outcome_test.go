package opsdoctor

import "testing"

// The shapes a model actually wrote at the end of its answer instead of
// making the call. Each must leave the reply without it and report the outcome.
func TestSplitWrittenOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, in, body, status, text string
	}{
		{
			name:   "json complete",
			in:     "The snapshot \"uitest1\" has been successfully created for resource r3 on node sdt3.\n\ntask_complete{\"result\": \"Snapshot 'uitest1' has been created for resource 'r3' on node 'sdt3'.\"}",
			body:   "The snapshot \"uitest1\" has been successfully created for resource r3 on node sdt3.",
			status: "complete",
			text:   "Snapshot 'uitest1' has been created for resource 'r3' on node 'sdt3'.",
		},
		{
			name:   "json blocked",
			in:     "The operator declined the command.\n\ntask_blocked{\"blocker\": \"The operator declined the sds_snapshot_create command for snapshot 'uitest2' on resource 'r3'. No further action can be taken without their approval.\"}",
			body:   "The operator declined the command.",
			status: "blocked",
			text:   "The operator declined the sds_snapshot_create command for snapshot 'uitest2' on resource 'r3'. No further action can be taken without their approval.",
		},
		{
			name:   "call syntax",
			in:     "This is a concrete blocker.\n\ntask_blocked(blocker=\"Operator declined the \\\"reject1\\\" snapshot\")",
			body:   "This is a concrete blocker.",
			status: "blocked",
			text:   "Operator declined the \"reject1\" snapshot",
		},
		{
			name:   "only the call",
			in:     "task_complete{\"result\": \"done\"}",
			body:   "",
			status: "complete",
			text:   "done",
		},
	} {
		body, o := splitWrittenOutcome(tc.in)
		if o == nil {
			t.Errorf("%s: no outcome found", tc.name)
			continue
		}
		if body != tc.body || o.Status != tc.status || o.Text != tc.text {
			t.Errorf("%s:\n body=%q\n outcome=%+v", tc.name, body, o)
		}
	}
}

// Naming the tools in prose is not calling them.
func TestSplitWrittenOutcomeLeavesProseAlone(t *testing.T) {
	for _, in := range []string{
		"Call task_complete when you are done.",
		"The agent uses task_blocked {when stuck} and then continues with more text.",
		"task_complete{not json}",
	} {
		if body, o := splitWrittenOutcome(in); o != nil || body != in {
			t.Errorf("%q: body=%q outcome=%+v", in, body, o)
		}
	}
}
