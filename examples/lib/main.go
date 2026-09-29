// Command lib-example shows how to embed steward as a library.
//
// It imports only the public facade (github.com/liliang-cn/steward) — never an
// internal package — so it mirrors how an external project would use it.
//
//	STEWARD_LLM_API_KEY=... STEWARD_DOMAIN_FILE=domain.toml STEWARD_KNOWLEDGE_DB_PATH=knowledge.db \
//	  go run ./examples/lib "how do I recover a degraded resource?"
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	steward "github.com/liliang-cn/steward"
)

func main() {
	question := strings.Join(os.Args[1:], " ")
	if question == "" {
		question = "How do I safely recover a resource stuck in a degraded state?"
	}

	// Zero-value fields fall back to STEWARD_* env vars, then defaults.
	a, err := steward.New(steward.Config{})
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
	_, sources, err := a.Stream(context.Background(), "", question, func(e steward.Event) {
		switch e.Kind {
		case steward.EventToolCall:
			fmt.Printf("\n[tool %s %v]\n", e.Tool, e.Args)
		case steward.EventReset:
			fmt.Print("\n\n(replacing preamble with final answer)\n\n")
		case steward.EventText:
			fmt.Print(e.Text)
		case steward.EventError:
			fmt.Fprintln(os.Stderr, "\n[error]", e.Text)
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nstream:", err)
		os.Exit(1)
	}
	fmt.Printf("\n\n(%d sources cited)\n", len(sources))
}
