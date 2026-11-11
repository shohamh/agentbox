// mockllm is a deterministic OpenAI-compatible model server that stands in for
// the internal vLLM in the container test bed. It implements chat completions
// and the Responses API (streaming and not), and plays a scripted coding agent:
// it inspects the tools a harness offers and answers with real tool calls, so
// Codex CLI and opencode run their full tool loop against it.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var debug = os.Getenv("MOCK_DEBUG") != ""

func main() {
	addr := envOr("LISTEN", ":8000")
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", models)
	mux.HandleFunc("/models", models)
	mux.HandleFunc("/v1/chat/completions", chat)
	mux.HandleFunc("/chat/completions", chat)
	mux.HandleFunc("/v1/responses", responses)
	mux.HandleFunc("/responses", responses)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	log.Printf("mockllm listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func id(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func models(w http.ResponseWriter, _ *http.Request) {
	var data []any
	for _, m := range []string{"deepseek-v3", "glm-4", "small"} {
		data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "agentbox-mock"})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---------- conversation model ----------

type tool struct {
	Kind   string // function | local_shell | custom
	Name   string
	Params map[string]any
}

type convo struct {
	System      string
	Task        string
	ToolOutputs []string
	Tools       []tool
}

type action struct {
	Text    string // final answer when Command is empty
	Command string
}

var runRe = regexp.MustCompile(`(?m)^\s*RUN:\s*(.+)$`)

func decide(c convo) action {
	if strings.Contains(c.System, "AGENTBOX-REVIEWER") || strings.Contains(c.Task, "AGENTBOX-REVIEWER") {
		return action{Text: review(c.System + "\n" + c.Task)}
	}
	n := len(c.ToolOutputs)
	last := ""
	if n > 0 {
		last = c.ToolOutputs[n-1]
	}
	var steps []string
	var final string
	if m := runRe.FindAllStringSubmatch(c.Task, -1); len(m) > 0 {
		for _, s := range m {
			steps = append(steps, strings.TrimSpace(s[1]))
		}
		final = fmt.Sprintf("Done. I ran %d command(s). Last output:\n%s", len(steps), truncate(last, 800))
	} else if strings.Contains(strings.ToLower(c.Task), "unit test") {
		steps = []string{
			"ls -la && sed -n '1,40p' calc.py",
			"cat > test_agent_added.py <<'EOF'\nimport unittest\n\nimport calc\n\n\nclass AgentAddedTest(unittest.TestCase):\n    def test_mul(self):\n        self.assertEqual(calc.mul(6, 7), 42)\n\n    def test_add_negative(self):\n        self.assertEqual(calc.add(-2, 5), 3)\n\n\nif __name__ == '__main__':\n    unittest.main()\nEOF\necho wrote test_agent_added.py",
			"python3 -m unittest discover -v 2>&1 | tail -15",
		}
		final = "I added test_agent_added.py with unit tests for calc.mul and calc.add, and ran the suite:\n" + truncate(last, 600)
	} else {
		return action{Text: "mock model reply: " + truncate(c.Task, 200)}
	}
	if n < len(steps) && len(c.Tools) > 0 {
		return action{Command: steps[n]}
	}
	return action{Text: final}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// review plays the reviewer model: strict JSON verdicts. A reason containing
// ESCALATE-RAW makes it answer with non-JSON to exercise the parse-failure path.
func review(text string) string {
	reason := text
	if i := strings.Index(text, "Stated reason:"); i >= 0 {
		reason = text[i:]
	}
	l := strings.ToLower(reason)
	switch {
	case strings.Contains(text, "ESCALATE-RAW"):
		return "I think this is probably fine but I am not sure."
	case strings.Contains(l, "personal") || strings.Contains(l, "exfil") || strings.Contains(l, "upload") || strings.Contains(l, "mining") || strings.Contains(l, "unusual"):
		return `{"verdict":"unusual","reason":"The stated reason does not match the user's team work and mentions moving data off the platform."}`
	default:
		return `{"verdict":"consistent","reason":"The request matches the user's role and recent repositories."}`
	}
}

func pickTool(tools []tool) (tool, bool) {
	for _, pref := range []string{"shell", "exec_command", "bash", "shell_command", "run_shell_command", "local_shell"} {
		for _, t := range tools {
			if t.Name == pref || (pref == "local_shell" && t.Kind == "local_shell") {
				return t, true
			}
		}
	}
	for _, t := range tools {
		if strings.Contains(strings.ToLower(t.Name), "shell") || strings.Contains(strings.ToLower(t.Name), "exec") || strings.Contains(strings.ToLower(t.Name), "bash") {
			return t, true
		}
	}
	return tool{}, false
}

// toolArgs builds arguments that satisfy the tool's JSON schema.
func toolArgs(t tool, cmd string) string {
	args := map[string]any{}
	props, _ := t.Params["properties"].(map[string]any)
	for name, raw := range props {
		p, _ := raw.(map[string]any)
		typ, _ := p["type"].(string)
		switch name {
		case "command", "cmd":
			if typ == "array" {
				args[name] = []string{"bash", "-lc", cmd}
			} else {
				args[name] = cmd
			}
		case "description":
			args[name] = "agentbox scripted step"
		case "workdir", "cwd":
			// leave to the harness default
		}
	}
	if len(args) == 0 {
		args["command"] = cmd
	}
	b, _ := json.Marshal(args)
	return string(b)
}

func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var sb strings.Builder
		for _, p := range x {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					sb.WriteString(s)
					sb.WriteString("\n")
				} else if s, ok := m["output"].(string); ok {
					sb.WriteString(s)
				}
			}
		}
		return sb.String()
	case map[string]any:
		if s, ok := x["text"].(string); ok {
			return s
		}
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ""
}

func usage(in []byte, out string) (int, int) {
	return len(in)/4 + 1, len(out)/4 + 1
}

// ---------- chat completions ----------

func chat(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if debug {
		log.Printf("chat request: %s", truncate(string(body), 4000))
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var c convo
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		role, _ := mm["role"].(string)
		switch role {
		case "system", "developer":
			c.System += textOf(mm["content"]) + "\n"
		case "user":
			t := textOf(mm["content"])
			if c.Task == "" || len(c.ToolOutputs) == 0 {
				c.Task = t
			}
		case "tool":
			c.ToolOutputs = append(c.ToolOutputs, textOf(mm["content"]))
		}
	}
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		fn, _ := tm["function"].(map[string]any)
		name, _ := fn["name"].(string)
		params, _ := fn["parameters"].(map[string]any)
		c.Tools = append(c.Tools, tool{Kind: "function", Name: name, Params: params})
	}
	model, _ := req["model"].(string)
	act := decide(c)
	var tc map[string]any
	if act.Command != "" {
		if t, ok := pickTool(c.Tools); ok {
			tc = map[string]any{"id": id("call_"), "type": "function", "function": map[string]any{"name": t.Name, "arguments": toolArgs(t, act.Command)}}
		} else {
			act.Text = "Run this: " + act.Command
		}
	}
	pt, ct := usage(body, act.Text+act.Command)
	u := map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct}
	cid := id("chatcmpl-")
	created := time.Now().Unix()
	finish := "stop"
	if tc != nil {
		finish = "tool_calls"
	}
	if stream, _ := req["stream"].(bool); !stream {
		msg := map[string]any{"role": "assistant", "content": act.Text}
		if tc != nil {
			msg["content"] = nil
			msg["tool_calls"] = []any{tc}
		}
		writeJSON(w, map[string]any{"id": cid, "object": "chat.completion", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}, "usage": u})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(delta map[string]any, fin any, extra map[string]any) {
		ch := map[string]any{"id": cid, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}}
		for k, v := range extra {
			ch[k] = v
		}
		b, _ := json.Marshal(ch)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	send(map[string]any{"role": "assistant", "content": ""}, nil, nil)
	if tc != nil {
		tcd := map[string]any{"index": 0, "id": tc["id"], "type": "function", "function": tc["function"]}
		send(map[string]any{"tool_calls": []any{tcd}}, nil, nil)
	} else {
		for _, part := range chunks(act.Text, 40) {
			send(map[string]any{"content": part}, nil, nil)
		}
	}
	send(map[string]any{}, finish, nil)
	if so, _ := req["stream_options"].(map[string]any); so != nil && so["include_usage"] == true {
		b, _ := json.Marshal(map[string]any{"id": cid, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{}, "usage": u})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

// ---------- Responses API ----------

func responses(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if debug {
		log.Printf("responses request: %s", truncate(string(body), 6000))
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var c convo
	if s, ok := req["instructions"].(string); ok {
		c.System = s
	}
	switch in := req["input"].(type) {
	case string:
		c.Task = in
	case []any:
		for _, it := range in {
			m, _ := it.(map[string]any)
			typ, _ := m["type"].(string)
			role, _ := m["role"].(string)
			switch {
			case strings.HasSuffix(typ, "_call_output"):
				c.ToolOutputs = append(c.ToolOutputs, textOf(m["output"]))
			case typ == "message" || (typ == "" && role != ""):
				t := textOf(m["content"])
				if role == "user" {
					// harnesses prepend environment context as user messages;
					// the task is the last user message before any tool output.
					if len(c.ToolOutputs) == 0 && !strings.HasPrefix(strings.TrimSpace(t), "<") {
						c.Task = t
					} else if c.Task == "" {
						c.Task = t
					}
				} else if role == "system" || role == "developer" {
					c.System += t + "\n"
				}
			}
		}
	}
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		kind, _ := tm["type"].(string)
		name, _ := tm["name"].(string)
		params, _ := tm["parameters"].(map[string]any)
		c.Tools = append(c.Tools, tool{Kind: kind, Name: name, Params: params})
	}
	model, _ := req["model"].(string)
	act := decide(c)
	var item map[string]any
	if act.Command != "" {
		if t, ok := pickTool(c.Tools); ok {
			if t.Kind == "local_shell" {
				item = map[string]any{"type": "local_shell_call", "id": id("lsh_"), "call_id": id("call_"), "status": "completed",
					"action": map[string]any{"type": "exec", "command": []string{"bash", "-lc", act.Command}}}
			} else {
				item = map[string]any{"type": "function_call", "id": id("fc_"), "call_id": id("call_"), "name": t.Name,
					"arguments": toolArgs(t, act.Command), "status": "completed"}
			}
		} else {
			act.Text = "Run this: " + act.Command
		}
	}
	if item == nil {
		item = map[string]any{"type": "message", "id": id("msg_"), "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": act.Text, "annotations": []any{}}}}
	}
	pt, ct := usage(body, act.Text+act.Command)
	rid := id("resp_")
	resp := map[string]any{"id": rid, "object": "response", "created_at": time.Now().Unix(), "status": "completed",
		"model": model, "output": []any{item}, "parallel_tool_calls": false, "tool_choice": "auto", "tools": []any{},
		"usage": map[string]any{"input_tokens": pt, "output_tokens": ct, "total_tokens": pt + ct,
			"input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}
	if stream, _ := req["stream"].(bool); !stream {
		writeJSON(w, resp)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	seq := 0
	ev := func(typ string, data map[string]any) {
		data["type"] = typ
		data["sequence_number"] = seq
		seq++
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
		if fl != nil {
			fl.Flush()
		}
	}
	inprog := map[string]any{}
	for k, v := range resp {
		inprog[k] = v
	}
	inprog["status"] = "in_progress"
	inprog["output"] = []any{}
	delete(inprog, "usage")
	ev("response.created", map[string]any{"response": inprog})
	ev("response.in_progress", map[string]any{"response": inprog})
	switch item["type"] {
	case "message":
		partial := map[string]any{"type": "message", "id": item["id"], "status": "in_progress", "role": "assistant", "content": []any{}}
		ev("response.output_item.added", map[string]any{"output_index": 0, "item": partial})
		ev("response.content_part.added", map[string]any{"item_id": item["id"], "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
		for _, p := range chunks(act.Text, 40) {
			ev("response.output_text.delta", map[string]any{"item_id": item["id"], "output_index": 0, "content_index": 0, "delta": p})
		}
		ev("response.output_text.done", map[string]any{"item_id": item["id"], "output_index": 0, "content_index": 0, "text": act.Text})
		ev("response.content_part.done", map[string]any{"item_id": item["id"], "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": act.Text, "annotations": []any{}}})
	case "function_call":
		partial := map[string]any{}
		for k, v := range item {
			partial[k] = v
		}
		partial["arguments"] = ""
		partial["status"] = "in_progress"
		ev("response.output_item.added", map[string]any{"output_index": 0, "item": partial})
		ev("response.function_call_arguments.delta", map[string]any{"item_id": item["id"], "output_index": 0, "delta": item["arguments"]})
		ev("response.function_call_arguments.done", map[string]any{"item_id": item["id"], "output_index": 0, "arguments": item["arguments"]})
	default:
		ev("response.output_item.added", map[string]any{"output_index": 0, "item": item})
	}
	ev("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	ev("response.completed", map[string]any{"response": resp})
}
