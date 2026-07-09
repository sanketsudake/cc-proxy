package markdown

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/audit"
	"github.com/sanketsudake/cc-proxy/internal/capture"
)

// Render produces the per-request markdown document, format-compatible with
// the original agent-proxy gist: meta, ranked audit table, redacted headers,
// system prompt, tool definitions, message history, decoded response.
func Render(rec *capture.Record) string {
	var b strings.Builder

	renderMeta(&b, rec)
	b.WriteString("\n\n")
	renderAudit(&b, rec)
	b.WriteString("\n\n")
	renderHeaders(&b, rec)

	var req requestBody
	_ = json.Unmarshal(rec.RawRequest, &req)

	if len(req.System) > 0 {
		b.WriteString("\n\n<system-prompt>\n\n")
		b.WriteString(renderSystem(req.System))
		b.WriteString("\n\n</system-prompt>")
	}
	if len(req.Tools) > 0 {
		b.WriteString("\n\n")
		renderTools(&b, req.Tools)
	}
	b.WriteString("\n\n")
	renderMessages(&b, req.Messages)
	b.WriteString("\n\n")
	renderResponse(&b, rec)
	b.WriteString("\n")
	return b.String()
}

// requestBody mirrors the renderable parts of a /v1/messages request.
type requestBody struct {
	System   json.RawMessage `json:"system"`
	Tools    []toolDef       `json:"tools"`
	Messages []message       `json:"messages"`
}

type toolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	Thinking     string          `json:"thinking"`
	Name         string          `json:"name"`
	ID           string          `json:"id"`
	Input        json.RawMessage `json:"input"`
	ToolUseID    string          `json:"tool_use_id"`
	IsError      bool            `json:"is_error"`
	Content      json.RawMessage `json:"content"`
	CacheControl json.RawMessage `json:"cache_control"`
	Source       *struct {
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

func renderMeta(b *strings.Builder, rec *capture.Record) {
	fmt.Fprintf(b, "<meta>\n\n")
	fmt.Fprintf(b, "- **timestamp**: %s\n", rec.Timestamp.UTC().Format(time.RFC3339))
	fmt.Fprintf(b, "- **model**: %s\n", orUnknown(rec.Model))
	fmt.Fprintf(b, "- **endpoint**: %s %s\n", rec.Method, rec.Endpoint)
	fmt.Fprintf(b, "- **upstream status**: %d\n", rec.StatusCode)
	fmt.Fprintf(b, "- **latency**: %dms (ttft %dms)\n", rec.LatencyMS, rec.TTFTMS)
	if rec.SessionID != "" {
		fmt.Fprintf(b, "- **session**: %s", rec.SessionID)
		if rec.App != "" {
			fmt.Fprintf(b, " (%s)", rec.App)
		}
		fmt.Fprintf(b, "\n")
	}
	if rec.AccountID != "" {
		fmt.Fprintf(b, "- **account**: %s\n", rec.AccountID)
	}
	if rec.RetryCount > 0 {
		fmt.Fprintf(b, "- **sdk retry**: attempt %d\n", rec.RetryCount)
	}
	if rec.Truncated {
		fmt.Fprintf(b, "- **truncated**: capture incomplete (client disconnect or size cap)\n")
	}
	fmt.Fprintf(b, "\n</meta>")
}

func renderAudit(b *strings.Builder, rec *capture.Record) {
	fmt.Fprintf(b, "<audit>\n\n")
	if in := rec.Usage.TotalInputTokens(); in > 0 {
		fmt.Fprintf(b, "**%s input tokens** billed for this request (from the response usage).\n\n", comma(in))
	}
	fmt.Fprintf(b, "- **tools**: %d definitions, %s bytes (~%s tokens)\n",
		len(rec.Tools), comma(rec.ToolsBytes), comma(audit.EstTokens(rec.ToolsBytes)))
	fmt.Fprintf(b, "- **system prompt**: %s bytes (~%s tokens)\n",
		comma(rec.SystemBytes), comma(audit.EstTokens(rec.SystemBytes)))
	fmt.Fprintf(b, "- **total request**: %s bytes\n", comma(rec.TotalBytes))
	if rec.CostKnown {
		fmt.Fprintf(b, "- **estimated cost**: $%.4f\n", rec.CostUSD)
	}
	fmt.Fprintf(b, "\n**Tools, ranked by size — this is your cut list:**\n\n")
	fmt.Fprintf(b, "| tool | bytes | ~tokens | %% of request |\n")
	fmt.Fprintf(b, "| --- | --: | --: | --: |\n")
	for _, t := range rec.Tools {
		pct := 0.0
		if rec.TotalBytes > 0 {
			pct = float64(t.Bytes) / float64(rec.TotalBytes) * 100
		}
		fmt.Fprintf(b, "| %s | %s | ~%s | %.1f%% |\n", t.Name, comma(t.Bytes), comma(t.ApproxTokens), pct)
	}
	fmt.Fprintf(b, "\n</audit>")
}

func renderHeaders(b *strings.Builder, rec *capture.Record) {
	keys := make([]string, 0, len(rec.Headers))
	for k := range rec.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "<headers>\n\n```\n")
	for _, k := range keys {
		fmt.Fprintf(b, "%s: %s\n", k, rec.Headers[k])
	}
	fmt.Fprintf(b, "```\n\n</headers>")
}

func renderSystem(system json.RawMessage) string {
	var s string
	if err := json.Unmarshal(system, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(system, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, bl := range blocks {
			text := bl.Text
			if bl.CacheControl != nil {
				text += "\n\n<!-- cache_control breakpoint -->"
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, "\n\n")
	}
	return fenceJSON(system)
}

func renderTools(b *strings.Builder, tools []toolDef) {
	fmt.Fprintf(b, "<tools>\n\n")
	for i, t := range tools {
		if i > 0 {
			b.WriteString("\n\n")
		}
		name := t.Name
		if name == "" {
			name = "(unnamed tool)"
		}
		fmt.Fprintf(b, "### %s\n", name)
		if t.Description != "" {
			fmt.Fprintf(b, "\n%s\n", t.Description)
		}
		if t.InputSchema != nil {
			fmt.Fprintf(b, "\n%s", fenceJSON(t.InputSchema))
		}
	}
	fmt.Fprintf(b, "\n\n</tools>")
}

func renderMessages(b *strings.Builder, messages []message) {
	fmt.Fprintf(b, "<messages>\n\n")
	for i, m := range messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		role := m.Role
		if role == "" {
			role = "unknown"
		}
		fmt.Fprintf(b, "<message index=\"%d\" role=\"%s\">\n\n", i+1, role)
		b.WriteString(renderContent(m.Content))
		fmt.Fprintf(b, "\n\n</message>")
	}
	fmt.Fprintf(b, "\n\n</messages>")
}

func renderContent(content json.RawMessage) string {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return fenceJSON(content)
	}
	parts := make([]string, 0, len(blocks))
	for _, bl := range blocks {
		parts = append(parts, renderBlock(bl))
	}
	return strings.Join(parts, "\n\n")
}

func renderBlock(bl contentBlock) string {
	switch bl.Type {
	case "text":
		return bl.Text
	case "thinking":
		return "<thinking>\n\n" + bl.Thinking + "\n\n</thinking>"
	case "tool_use":
		return fmt.Sprintf("<tool-use name=%q id=%q>\n\n%s\n\n</tool-use>", bl.Name, bl.ID, fenceJSON(bl.Input))
	case "tool_result":
		return fmt.Sprintf("<tool-result tool-use-id=%q is-error=\"%v\">\n\n%s\n\n</tool-result>",
			bl.ToolUseID, bl.IsError, renderContent(bl.Content))
	case "image":
		return imagePlaceholder(bl)
	default:
		raw, _ := json.Marshal(bl)
		return fenceJSON(raw)
	}
}

func imagePlaceholder(bl contentBlock) string {
	media, size := "unknown", 0
	if bl.Source != nil {
		media = bl.Source.MediaType
		size = len(bl.Source.Data)
	}
	return fmt.Sprintf("`[image: %s, %d base64 chars — full data in .request.txt]`", media, size)
}

func renderResponse(b *strings.Builder, rec *capture.Record) {
	fmt.Fprintf(b, "<response>\n\n")
	var parts []string
	if rec.StopReason != "" {
		parts = append(parts, fmt.Sprintf("- **stop reason**: %s", rec.StopReason))
	}
	if u := rec.Usage; u.TotalInputTokens()+u.OutputTokens > 0 {
		usage, _ := json.Marshal(u)
		parts = append(parts, fmt.Sprintf("- **usage**: %s", usage))
	}
	if rec.ResponseError != "" {
		parts = append(parts, fmt.Sprintf("- **error**: %s", rec.ResponseError))
	}
	for _, bl := range rec.ResponseBlocks {
		switch bl.Kind {
		case "text":
			parts = append(parts, "<assistant-text>\n\n"+bl.Text+"\n\n</assistant-text>")
		case "thinking":
			parts = append(parts, "<thinking>\n\n"+bl.Text+"\n\n</thinking>")
		case "tool_use":
			body := bl.Text
			if body == "" {
				body = "{}"
			}
			parts = append(parts, fmt.Sprintf("<tool-use name=%q id=%q>\n\n```json\n%s\n```\n\n</tool-use>", bl.Name, bl.ID, body))
		default:
			parts = append(parts, fmt.Sprintf("<block kind=%q>\n\n%s\n\n</block>", bl.Kind, bl.Text))
		}
	}
	b.WriteString(strings.Join(parts, "\n\n"))
	fmt.Fprintf(b, "\n\n</response>")
}

func fenceJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "```\n" + string(raw) + "\n```"
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "```\n" + string(raw) + "\n```"
	}
	return "```json\n" + string(pretty) + "\n```"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// comma renders n with thousands separators.
func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
