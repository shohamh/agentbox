// agentbox-preflight checks a LiteLLM proxy before Agentbox's gateway is
// pointed at it. See docs/litellm-admin.md.
//
//	agentbox-preflight --url https://litellm.corp --master-key-file /run/secrets/litellm_master_key \
//	    --models claude-sonnet,claude-haiku,gpt-5
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"agentbox/pkg/llmcheck"
)

func main() {
	url := flag.String("url", os.Getenv("LITELLM_URL"), "LiteLLM base URL (env LITELLM_URL)")
	keyFile := flag.String("master-key-file", os.Getenv("LITELLM_MASTER_KEY_FILE"), "file holding the key the gateway will use (env LITELLM_MASTER_KEY_FILE)")
	models := flag.String("models", "", "comma-separated model names the groups may use, as callers name them")
	ttl := flag.Duration("ttl", 15*time.Minute, "key lifetime the gateway will request (KEY_TTL)")
	noCompletions := flag.Bool("no-completions", false, "do not send test requests (skips the API checks)")
	noExpiry := flag.Bool("no-expiry", false, "skip the 5-second expiry proof")
	flag.Parse()

	key := os.Getenv("LITELLM_MASTER_KEY")
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "master key file:", err)
			os.Exit(2)
		}
		key = strings.TrimSpace(string(b))
	}
	if *url == "" || key == "" {
		fmt.Fprintln(os.Stderr, "need --url and a master key (--master-key-file, or LITELLM_MASTER_KEY in the environment)")
		os.Exit(2)
	}
	var ms []string
	for _, m := range strings.Split(*models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			ms = append(ms, m)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res := llmcheck.Run(ctx, llmcheck.Config{URL: *url, MasterKey: key, Models: ms, TTL: *ttl, Completions: !*noCompletions, Expiry: !*noExpiry})
	for _, r := range res {
		line := fmt.Sprintf("[%-4s] %s", r.Status, r.Name)
		if r.Detail != "" {
			line += ": " + r.Detail
		}
		fmt.Println(line)
		if r.Status == llmcheck.Fail || r.Status == llmcheck.Warn {
			if r.Fix != "" {
				fmt.Println("       fix:", r.Fix)
			}
		}
	}
	if llmcheck.Failed(res) {
		fmt.Println("\nLiteLLM is NOT ready for Agentbox.")
		os.Exit(1)
	}
	fmt.Println("\nLiteLLM is ready for Agentbox.")
}
