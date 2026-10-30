package assistant

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI serves scripted streams, one per request, and records requests.
type fakeAPI struct {
	t       *testing.T
	mu      sync.Mutex
	streams [][]string
	reqs    []map[string]any
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer sk-test" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var req map[string]any
	if err := json.Unmarshal(b, &req); err != nil {
		f.t.Errorf("decode request: %v", err)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	var events []string
	if len(f.streams) > 0 {
		events, f.streams = f.streams[0], f.streams[1:]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var typ struct {
			Type string `json:"type"`
		}
		json.Unmarshal([]byte(e), &typ)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, e)
	}
}

func newFake(t *testing.T, streams ...[]string) (*fakeAPI, *Client) {
	f := &fakeAPI{t: t, streams: streams}
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return f, &Client{BaseURL: ts.URL + "/v1/", APIKey: "sk-test", HTTP: ts.Client()}
}

func completed(in, out int) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":%d,"output_tokens":%d}}}`, in, out)
}

func echoTool() Tool {
	return Tool{
		Name: "echo", Description: "returns its arguments",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "integer"}}},
		Call: func(_ context.Context, args jsontext.Value) (any, error) {
			var a struct {
				X int `json:"x"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, err
			}
			if a.X < 0 {
				return nil, errors.New("negative")
			}
			return map[string]int{"x": a.X}, nil
		},
	}
}

func TestRunWithTools(t *testing.T) {
	f, c := newFake(t,
		[]string{
			`{"type":"response.created","response":{"id":"resp_1"}}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"需要查询"}`,
			`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc"}}`,
			`{"type":"response.output_text.delta","delta":"让我"}`,
			`{"type":"response.output_text.delta","delta":"查一下"}`,
			`{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"让我查一下"}]}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"echo","arguments":"{\"x\":1}"}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_2","call_id":"c2","name":"echo","arguments":"{\"x\":-1}"}}`,
			completed(100, 10),
		},
		[]string{
			`{"type":"response.output_text.delta","delta":"结果是 1"}`,
			completed(200, 20),
		},
	)
	var events []string
	res, err := c.Run(t.Context(), Request{
		Model: "gpt-test", Instructions: "be brief", Input: []Item{UserMessage("x 是多少")}, Tools: []Tool{echoTool()}, MaxRounds: 4,
	}, func(e Event) {
		s := e.Type + ":" + e.Text
		if e.Call != nil {
			s += e.Call.ID + "=" + e.Call.Result
		}
		events = append(events, s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "让我查一下\n\n结果是 1" || res.Reasoning != "需要查询" || res.InputTokens != 300 || res.OutputTokens != 30 {
		t.Errorf("result = %+v", res)
	}
	if len(res.ToolCalls) != 2 || res.ToolCalls[0].Result != `{"x":1}` || res.ToolCalls[0].Error ||
		!res.ToolCalls[1].Error || !strings.Contains(res.ToolCalls[1].Result, "negative") {
		t.Errorf("tool calls = %+v", res.ToolCalls)
	}
	want := []string{"reasoning:需要查询", "content:让我", "content:查一下", "tool_call:c1=", `tool_result:c1={"x":1}`,
		"tool_call:c2=", `tool_result:c2={"error":"negative"}`, "content:\n\n结果是 1"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Errorf("events =\n%q\nwant\n%q", events, want)
	}

	if len(f.reqs) != 2 {
		t.Fatalf("%d requests", len(f.reqs))
	}
	r0, r1 := f.reqs[0], f.reqs[1]
	if r0["model"] != "gpt-test" || r0["instructions"] != "be brief" || r0["store"] != false || r0["stream"] != true ||
		r0["tool_choice"] != "auto" || len(r0["tools"].([]any)) != 1 {
		t.Errorf("first request = %v", r0)
	}
	in, _ := json.Marshal(r1["input"], json.Deterministic(true))
	for _, s := range []string{`"encrypted_content":"enc"`, `{"content":"让我查一下","role":"assistant"}`,
		`{"arguments":"{\"x\":1}","call_id":"c1","name":"echo","type":"function_call"}`,
		`{"call_id":"c1","output":"{\"x\":1}","type":"function_call_output"}`, `"call_id":"c2"`} {
		if !strings.Contains(string(in), s) {
			t.Errorf("second input lacks %s:\n%s", s, in)
		}
	}
	if strings.Contains(string(in), "fc_1") {
		t.Errorf("function call item IDs must not be replayed with store=false: %s", in)
	}
}

func TestRunLastRoundCannotCallTools(t *testing.T) {
	f, c := newFake(t, []string{
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"}}`,
		completed(1, 1),
	})
	res, err := c.Run(t.Context(), Request{Model: "m", Input: []Item{UserMessage("hi")}, Tools: []Tool{echoTool()}, MaxRounds: 1}, func(Event) {})
	if err != nil || len(res.ToolCalls) != 0 {
		t.Errorf("Run = %+v, %v", res, err)
	}
	if f.reqs[0]["tool_choice"] != "none" {
		t.Errorf("tool_choice = %v, want none", f.reqs[0]["tool_choice"])
	}
}

func TestRunMessageWithoutDeltas(t *testing.T) {
	_, c := newFake(t, []string{
		`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"你好"}]}}`,
		completed(1, 1),
	})
	var got string
	res, err := c.Run(t.Context(), Request{Model: "m", Input: []Item{UserMessage("hi")}}, func(e Event) { got += e.Text })
	if err != nil || res.Content != "你好" || got != "你好" {
		t.Errorf("Run = %+v, %v, events %q", res, err, got)
	}
}

func TestRunErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"invalid_api_key","message":"Incorrect API key"}}`)
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, APIKey: "bad"}
	_, err := c.Run(t.Context(), Request{Model: "m", Input: []Item{UserMessage("hi")}}, func(Event) {})
	if ae, ok := errors.AsType[*APIError](err); !ok || ae.Status != 401 || ae.Code != "invalid_api_key" {
		t.Errorf("HTTP error = %v", err)
	}

	for name, stream := range map[string][]string{
		"error event": {`{"type":"response.output_text.delta","delta":"部分"}`, `{"type":"error","code":"server_error","message":"boom"}`},
		"failed":      {`{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"slow down"}}}`},
		"incomplete":  {`{"type":"response.output_text.delta","delta":"部分"}`, `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`},
		"truncated":   {`{"type":"response.output_text.delta","delta":"部分"}`},
	} {
		_, c := newFake(t, stream)
		res, err := c.Run(t.Context(), Request{Model: "m", Input: []Item{UserMessage("hi")}}, func(Event) {})
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if strings.Contains(stream[0], "部分") && res.Content != "部分" {
			t.Errorf("%s: partial content = %q", name, res.Content)
		}
	}
}

func TestReasoningRequest(t *testing.T) {
	f, c := newFake(t, []string{completed(1, 1)})
	c.ReasoningEffort = "low"
	if _, err := c.Run(t.Context(), Request{Model: "m", Input: []Item{UserMessage("hi")}}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f.reqs[0], json.Deterministic(true))
	if !strings.Contains(string(b), `"reasoning":{"effort":"low","summary":"auto"}`) ||
		!strings.Contains(string(b), `"include":["reasoning.encrypted_content"]`) || strings.Contains(string(b), "tool_choice") {
		t.Errorf("request = %s", b)
	}
}

func TestReadEvents(t *testing.T) {
	var got []string
	in := ": comment\r\nevent: x\r\ndata: {\"a\":\r\ndata: 1}\r\n\r\ndata: [DONE]\n\ndata: after\n\n"
	if err := readEvents(strings.NewReader(in), func(b []byte) error { got = append(got, string(b)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "{\"a\":\n1}" {
		t.Errorf("events = %q", got)
	}
}
