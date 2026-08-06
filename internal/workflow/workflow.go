// Package workflow rewrites Ultracode/Workflow tool scripts so every agent()
// call sets stallMs to the maximum safe value. Workflow has a private 180-second
// default that is independent of API_TIMEOUT_MS and CLAUDE_ASYNC_AGENT_STALL_TIMEOUT_MS;
// without the rewrite a slow remote model aborts background fan-out while quiet.
//
// The rewriter is a proper JavaScript tokenizer that understands strings,
// template literals, comments and nested delimiters, so commas or parentheses
// inside an agent prompt can never fool the rewrite — unlike a naive regex which
// corrupts scripts containing a ')' inside a string literal.
package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// NoTimeoutMS is JavaScript's maximum safe timer value: ~24.8 days, effectively
// no inference deadline while still avoiding the immediate-failure overflow.
const NoTimeoutMS = 2147483647

// SystemPrompt is the belt-and-suspenders instruction appended to the session
// system prompt. It asks the model to set stallMs itself; the hook rewriter
// makes it deterministic. It also directs agents to the DuckDuckGo MCP for web
// research instead of the Anthropic-only WebSearch tool (which is disabled on
// the local ultra-zen proxy), so delegated research is never routed around a
// denied tool.
const SystemPrompt = "You are using a slow remote model through ultra-zen. Never abort a model request or agent merely because it is quiet while processing a prompt. For every Workflow tool script, set stallMs: 2147483647 in every agent() options object. This is required even for foreground agents and retries. For web research, use the DuckDuckGo MCP tools mcp__ddg-search__search and mcp__ddg-search__fetch_content. Do not use or mention the built-in WebSearch tool — it is disabled in this environment."

// HookInput is the JSON Claude Code sends to a PreToolUse hook on stdin.
type HookInput struct {
	ToolName  string                 `json:"tool_name"`
	ToolInput map[string]interface{} `json:"tool_input"`
}

// RunHook reads a PreToolUse hook payload from stdin, rewrites any Workflow
// script to add the maximum safe stallMs to every agent() call, and writes the
// updated input to stdout in the hook-specific output shape. It is the entry
// point for the `ultra-zen workflow-hook` subcommand.
func RunHook() {
	var input HookInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Fprintf(os.Stderr, "ultra-zen workflow hook: %v\n", err)
		fmt.Println(`{}`)
		return
	}
	if input.ToolName != "Workflow" || input.ToolInput == nil {
		fmt.Println(`{}`)
		return
	}
	if script, ok := input.ToolInput["script"].(string); ok {
		input.ToolInput["script"] = NoTimeoutScript(script)
	}
	output := map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": "ultra-zen Workflow policy",
			"updatedInput":             input.ToolInput,
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(output)
}

type scriptEdit struct {
	start, end int
	text       string
}

// NoTimeoutScript adds the maximum safe stallMs to every direct agent(...) call.
// It understands strings, template literals, comments and nested delimiters, so
// commas in a long agent prompt cannot fool the rewrite.
func NoTimeoutScript(script string) string {
	var edits []scriptEdit
	for i := 0; i < len(script); {
		switch {
		case script[i] == '\'' || script[i] == '"':
			i = skipJSQuoted(script, i, script[i])
		case script[i] == '`':
			i = skipJSTemplate(script, i)
		case i+1 < len(script) && script[i:i+2] == "//":
			i = skipJSLineComment(script, i)
		case i+1 < len(script) && script[i:i+2] == "/*":
			i = skipJSBlockComment(script, i)
		case isJSIdentStart(script[i]):
			start := i
			i++
			for i < len(script) && isJSIdentPart(script[i]) {
				i++
			}
			if script[start:i] != "agent" || previousNonSpace(script, start) == '.' {
				continue
			}
			open := skipJSSpace(script, i)
			if open >= len(script) || script[open] != '(' {
				continue
			}
			close, commas, ok := scanJSCall(script, open)
			if !ok {
				continue
			}
			if len(commas) == 0 {
				edits = append(edits, scriptEdit{start: close, end: close, text: fmt.Sprintf(", { stallMs: %d }", NoTimeoutMS)})
				i = close + 1
				continue
			}

			secondStart := skipJSSpace(script, commas[0]+1)
			secondEnd := close
			if len(commas) > 1 {
				secondEnd = commas[1]
			}
			trimmedEnd := trimJSSpaceBack(script, secondEnd)
			if secondStart >= trimmedEnd {
				edits = append(edits, scriptEdit{start: secondStart, end: secondStart, text: fmt.Sprintf("{ stallMs: %d }", NoTimeoutMS)})
			} else {
				expr := strings.TrimSpace(script[secondStart:trimmedEnd])
				compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(expr)
				if !strings.Contains(compact, fmt.Sprintf("stallMs:%d", NoTimeoutMS)) {
					// Wrapping is safer than inserting a property into an object literal:
					// comments, spreads and an existing lower stallMs all remain valid,
					// while the final Object.assign operand deterministically wins.
					replacement := fmt.Sprintf("Object.assign({}, (%s), { stallMs: %d })", expr, NoTimeoutMS)
					edits = append(edits, scriptEdit{start: secondStart, end: trimmedEnd, text: replacement})
				}
			}
			i = close + 1
		default:
			i++
		}
	}
	if len(edits) == 0 {
		return script
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, edit := range edits {
		script = script[:edit.start] + edit.text + script[edit.end:]
	}
	return script
}

func scanJSCall(s string, open int) (int, []int, bool) {
	paren, brace, bracket := 1, 0, 0
	var commas []int
	for i := open + 1; i < len(s); {
		switch {
		case s[i] == '\'' || s[i] == '"':
			i = skipJSQuoted(s, i, s[i])
		case s[i] == '`':
			i = skipJSTemplate(s, i)
		case i+1 < len(s) && s[i:i+2] == "//":
			i = skipJSLineComment(s, i)
		case i+1 < len(s) && s[i:i+2] == "/*":
			i = skipJSBlockComment(s, i)
		default:
			switch s[i] {
			case '(':
				paren++
			case ')':
				paren--
				if paren == 0 {
					return i, commas, true
				}
			case '{':
				brace++
			case '}':
				if brace > 0 {
					brace--
				}
			case '[':
				bracket++
			case ']':
				if bracket > 0 {
					bracket--
				}
			case ',':
				if paren == 1 && brace == 0 && bracket == 0 {
					commas = append(commas, i)
				}
			}
			i++
		}
	}
	return 0, nil, false
}

func skipJSQuoted(s string, i int, quote byte) int {
	for i++; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			return i + 1
		}
	}
	return len(s)
}

func skipJSTemplate(s string, i int) int {
	for i++; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '`' {
			return i + 1
		}
	}
	return len(s)
}

func skipJSLineComment(s string, i int) int {
	if nl := strings.IndexByte(s[i+2:], '\n'); nl >= 0 {
		return i + 2 + nl + 1
	}
	return len(s)
}

func skipJSBlockComment(s string, i int) int {
	if end := strings.Index(s[i+2:], "*/"); end >= 0 {
		return i + 2 + end + 2
	}
	return len(s)
}

func skipJSSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
		i++
	}
	return i
}

func trimJSSpaceBack(s string, i int) int {
	for i > 0 && (s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == '\r' || s[i-1] == '\n') {
		i--
	}
	return i
}

func previousNonSpace(s string, i int) byte {
	for i--; i >= 0; i-- {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\r' && s[i] != '\n' {
			return s[i]
		}
	}
	return 0
}

func isJSIdentStart(b byte) bool {
	return b == '_' || b == '$' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func isJSIdentPart(b byte) bool {
	return isJSIdentStart(b) || b >= '0' && b <= '9'
}
