// Package assistant runs a conversation turn with an OpenAI model through the
// Responses API: it streams the answer and executes the function tools the
// model calls until the model answers in text.
//
// Requests are stateless (store=false). The caller passes the whole
// conversation as input items and keeps the history itself.
package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON schema of the arguments object.
	Parameters map[string]any
	// Call runs the tool with the arguments decoded from the model's JSON.
	// The result is encoded as JSON for the model.
	Call func(ctx context.Context, args jsontext.Value) (any, error)
}

// Item is an input item of the Responses API, as JSON.
type Item = jsontext.Value

// item encodes v with sorted keys, so that repeated requests share a
// prefix the API can cache.
func item(v any) Item {
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		panic(err) // only maps of strings are encoded
	}
	return b
}

// UserMessage returns an input item with a user message.
func UserMessage(text string) Item {
	return item(map[string]string{"role": "user", "content": text})
}

// AssistantMessage returns an input item with an earlier answer.
func AssistantMessage(text string) Item {
	return item(map[string]string{"role": "assistant", "content": text})
}

// FunctionCall returns an input item with an earlier function call.
func FunctionCall(callID, name, arguments string) Item {
	return item(map[string]string{"type": "function_call", "call_id": callID, "name": name, "arguments": arguments})
}

// FunctionOutput returns an input item with the output of a function call.
func FunctionOutput(callID, output string) Item {
	return item(map[string]string{"type": "function_call_output", "call_id": callID, "output": output})
}

// Client calls the Responses API.
type Client struct {
	// BaseURL is the API root, for example https://api.openai.com/v1.
	BaseURL string
	APIKey  string
	// ReasoningEffort is sent to reasoning models when not empty, together
	// with a request for reasoning summaries.
	ReasoningEffort string
	// HTTP sends requests; default a client that honors proxy variables.
	HTTP *http.Client
}

// Request is one conversation turn.
type Request struct {
	Model        string
	Instructions string
	// Input is the conversation so far, ending with the new user message.
	Input []Item
	Tools []Tool
	// MaxRounds bounds the model requests; the last one may not call tools.
	MaxRounds int
	// Timeout bounds each model request.
	Timeout time.Duration
	// ToolTimeout bounds each tool call; default 30s.
	ToolTimeout time.Duration
}

// Event types emitted while a turn runs.
const (
	EventContent    = "content"
	EventReasoning  = "reasoning"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
)

// Event is progress of a turn.
type Event struct {
	Type string
	// Text is the delta of EventContent and EventReasoning.
	Text string
	// Call is set for EventToolCall (without result) and EventToolResult.
	Call *ToolCall
}

// ToolCall is a function call made by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
	Result    string
	Error     bool
}

// Result is the outcome of a turn. It is returned with partial content when
// the turn fails.
type Result struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	InputTokens  int64
	OutputTokens int64
}

// APIError is an error reported by the API.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("openai")
	if e.Status != 0 {
		fmt.Fprintf(&b, " HTTP %d", e.Status)
	}
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	return b.String() + ": " + e.Message
}

// maxToolOutput bounds the JSON returned by a tool to the model.
const maxToolOutput = 48 << 10

// Run executes a turn: it requests answers, runs the tools the model calls
// and feeds their outputs back, at most MaxRounds times.
func (c *Client) Run(ctx context.Context, req Request, emit func(Event)) (*Result, error) {
	if req.MaxRounds < 1 {
		req.MaxRounds = 1
	}
	if req.ToolTimeout <= 0 {
		req.ToolTimeout = 30 * time.Second
	}
	tools := make(map[string]*Tool, len(req.Tools))
	for i := range req.Tools {
		tools[req.Tools[i].Name] = &req.Tools[i]
	}
	res := &Result{}
	input := append([]Item(nil), req.Input...)
	for round := range req.MaxRounds {
		last := round == req.MaxRounds-1
		out, err := c.stream(ctx, &req, input, last, res, emit)
		if out != nil {
			res.InputTokens += out.inputTokens
			res.OutputTokens += out.outputTokens
			input = append(input, out.items...)
		}
		if err != nil {
			return res, err
		}
		if len(out.calls) == 0 || last {
			return res, nil
		}
		for _, call := range out.calls {
			emit(Event{Type: EventToolCall, Call: &call})
			call.Result, call.Error = runTool(ctx, tools[call.Name], call, req.ToolTimeout)
			res.ToolCalls = append(res.ToolCalls, call)
			emit(Event{Type: EventToolResult, Call: &call})
			input = append(input, FunctionOutput(call.ID, call.Result))
		}
	}
	return res, nil
}

func runTool(ctx context.Context, t *Tool, call ToolCall, timeout time.Duration) (string, bool) {
	fail := func(err error) (string, bool) {
		return string(item(map[string]string{"error": err.Error()})), true
	}
	if t == nil {
		return fail(fmt.Errorf("unknown tool %q", call.Name))
	}
	args := jsontext.Value(call.Arguments)
	if strings.TrimSpace(call.Arguments) == "" {
		args = jsontext.Value("{}")
	}
	if !args.IsValid() {
		return fail(errors.New("arguments are not valid JSON"))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	v, err := t.Call(ctx, args)
	if err != nil {
		return fail(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fail(fmt.Errorf("encode result: %w", err))
	}
	if len(b) > maxToolOutput {
		return string(item(map[string]string{
			"error":   "the result is too large; narrow the query",
			"partial": truncate(string(b), maxToolOutput/2),
		})), true
	}
	return string(b), false
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

type toolDef struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

type reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
}

type responsesRequest struct {
	Model        string     `json:"model"`
	Instructions string     `json:"instructions,omitzero"`
	Input        []Item     `json:"input"`
	Tools        []toolDef  `json:"tools,omitzero"`
	ToolChoice   string     `json:"tool_choice,omitzero"`
	Reasoning    *reasoning `json:"reasoning,omitzero"`
	Include      []string   `json:"include,omitzero"`
	Stream       bool       `json:"stream"`
	Store        bool       `json:"store"`
}

type usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type streamEvent struct {
	Type     string         `json:"type"`
	Delta    string         `json:"delta"`
	Item     jsontext.Value `json:"item"`
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Error    *apiErrorBody  `json:"error"`
	Response *struct {
		Usage             *usage        `json:"usage"`
		Error             *apiErrorBody `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

type outputItem struct {
	Type             string `json:"type"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
	Content          []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

type roundOutput struct {
	items                     []Item
	calls                     []ToolCall
	inputTokens, outputTokens int64
}

// stream sends one request and relays its text as it arrives.
func (c *Client) stream(ctx context.Context, req *Request, input []Item, last bool, res *Result, emit func(Event)) (*roundOutput, error) {
	body := responsesRequest{
		Model: req.Model, Instructions: req.Instructions, Input: input, ToolChoice: "auto", Stream: true,
	}
	if last {
		body.ToolChoice = "none"
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, toolDef{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	if len(body.Tools) == 0 {
		body.ToolChoice = ""
	}
	if c.ReasoningEffort != "" {
		body.Reasoning = &reasoning{Effort: c.ReasoningEffort, Summary: "auto"}
		// Without stored responses, reasoning is carried across tool calls
		// in its encrypted form.
		body.Include = []string{"reasoning.encrypted_content"}
	}
	b, err := json.Marshal(body, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/responses", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "text/event-stream")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, readAPIError(resp)
	}

	out := &roundOutput{}
	var text, summary strings.Builder
	sep := func(b *strings.Builder, total string) string {
		// Separate the text of consecutive rounds by a blank line.
		if b.Len() == 0 && total != "" && !strings.HasSuffix(total, "\n\n") {
			return "\n\n"
		}
		return ""
	}
	addText := func(t string) {
		if t == "" {
			return
		}
		t = sep(&text, res.Content) + t
		text.WriteString(t)
		res.Content += t
		emit(Event{Type: EventContent, Text: t})
	}
	err = readEvents(resp.Body, func(data []byte) error {
		var ev streamEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return fmt.Errorf("openai: decode event: %w", err)
		}
		switch ev.Type {
		case "response.output_text.delta", "response.refusal.delta":
			addText(ev.Delta)
		case "response.reasoning_summary_text.delta":
			d := sep(&summary, res.Reasoning) + ev.Delta
			summary.WriteString(d)
			res.Reasoning += d
			emit(Event{Type: EventReasoning, Text: d})
		case "response.output_item.done":
			var it outputItem
			if err := json.Unmarshal(ev.Item, &it); err != nil {
				return fmt.Errorf("openai: decode output item: %w", err)
			}
			switch it.Type {
			case "function_call":
				out.calls = append(out.calls, ToolCall{ID: it.CallID, Name: it.Name, Arguments: it.Arguments})
				out.items = append(out.items, FunctionCall(it.CallID, it.Name, it.Arguments))
			case "reasoning":
				if it.EncryptedContent != "" {
					out.items = append(out.items, bytes.Clone(ev.Item))
				}
			case "message":
				var msg strings.Builder
				for _, p := range it.Content {
					msg.WriteString(p.Text + p.Refusal)
				}
				if text.Len() == 0 {
					addText(msg.String()) // the stream carried no deltas
				}
				if msg.Len() > 0 {
					out.items = append(out.items, AssistantMessage(msg.String()))
				}
			}
		case "response.completed", "response.incomplete", "response.failed":
			if r := ev.Response; r != nil {
				if r.Usage != nil {
					out.inputTokens, out.outputTokens = r.Usage.InputTokens, r.Usage.OutputTokens
				}
				switch {
				case r.Error != nil:
					return &APIError{Code: r.Error.Code, Message: r.Error.Message}
				case ev.Type == "response.incomplete" && r.IncompleteDetails != nil:
					return &APIError{Code: "incomplete", Message: "the answer is incomplete: " + r.IncompleteDetails.Reason}
				}
			}
			if ev.Type == "response.failed" {
				return &APIError{Code: "failed", Message: "the response failed"}
			}
			return errDone
		case "error":
			e := apiErrorBody{Code: ev.Code, Message: ev.Message}
			if ev.Error != nil {
				e = *ev.Error
			}
			return &APIError{Code: e.Code, Message: e.Message}
		}
		return nil
	})
	if errors.Is(err, errDone) {
		return out, nil
	}
	if err == nil {
		err = errors.New("openai: the stream ended before the response completed")
	}
	if ctx.Err() != nil && !errors.Is(err, context.Canceled) {
		err = fmt.Errorf("%w (%w)", err, context.Cause(ctx))
	}
	return out, err
}

var errDone = errors.New("done")

// readEvents calls fn with the data of every server-sent event until fn
// returns an error or the stream ends.
func readEvents(r io.Reader, fn func([]byte) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var data []byte
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
				if len(data) > 0 {
					if string(data) == "[DONE]" {
						return nil
					}
					if e := fn(data); e != nil {
						return e
					}
					data = data[:0]
				}
			case bytes.HasPrefix(line, []byte("data:")):
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(data) > 0 {
					return fn(data)
				}
				return nil
			}
			return fmt.Errorf("openai: read stream: %w", err)
		}
	}
}

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Error *apiErrorBody `json:"error"`
	}
	e := &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(truncate(string(b), 500))}
	if json.Unmarshal(b, &body) == nil && body.Error != nil {
		e.Code, e.Message = body.Error.Code, body.Error.Message
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	return e
}
