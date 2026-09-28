// Command lib-example shows how to embed opspilot as a library.
//
// It imports only the public facade (github.com/liliang-cn/opspilot) — never an
// internal package — so it mirrors how an external project would use it.
//
//	OPSPILOT_LLM_API_KEY=... OPSPILOT_DOMAIN_FILE=domain.toml OPSPILOT_KNOWLEDGE_DB_PATH=knowledge.db \
//	  go run ./examples/lib "how do I recover a degraded resource?"
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	opspilot "github.com/liliang-cn/opspilot"
)

func main() {
	question := strings.Join(os.Args[1:], " ")
	if question == "" {
		question = "How do I safely recover a resource stuck in a degraded state?"
	}

	// Zero-value fields fall back to OPSPILOT_* env vars, then defaults.
	a, err := opspilot.New(opspilot.Config{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}
	defer a.Close()

	fmt.Printf("domain: %s\n\n", a.Domain().Name)

	// 1) One-shot retrieval (no LLM) — inspect what the agent would ground on.
	if hits, err := a.Search(context.Background(), question, 4); err == nil {
		fmt.Println("top sources:")
		for _, h := range hits {
			fmt.Printf("  - %s\n", h.DocumentID)
		}
		fmt.Println()
	}

	// 2) The deterministic safety wall is usable on its own.
	if v := a.CheckCommand("wipefs -a /dev/sdb"); v.Blocked {
		fmt.Printf("safety: blocked %q (%s)\n\n", "wipefs -a /dev/sdb", v.Reason)
	}

	// 3) Stream a grounded answer, printing tool calls live.
	fmt.Println("--- answer ---")
	_, sources, err := a.Stream(context.Background(), "", question, func(e opspilot.Event) {
		switch e.Kind {
		case opspilot.EventToolCall:
			fmt.Printf("\n[tool %s %v]\n", e.Tool, e.Args)
		case opspilot.EventReset:
			fmt.Print("\n\n(replacing preamble with final answer)\n\n")
		case opspilot.EventText:
			fmt.Print(e.Text)
		case opspilot.EventError:
			fmt.Fprintln(os.Stderr, "\n[error]", e.Text)
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nstream:", err)
		os.Exit(1)
	}
	fmt.Printf("\n\n(%d sources cited)\n", len(sources))
}
