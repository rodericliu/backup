package server

import (
	"cmp"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rodericliu/backup/internal/server/assistant"
	"github.com/rodericliu/backup/internal/server/config"
	"github.com/rodericliu/backup/internal/store"
)

// The AI assistant answers questions about backups with read-only tools.
// Conversations are stored per user; the model gets the history of the
// session with every question.

const auditAssistantChat = "assistant_chat"

const (
	// maxQuestion bounds a question in runes.
	maxQuestion = 4000
	// maxAssistantSessions bounds the sessions listed for a user.
	maxAssistantSessions = 100
	// maxHistory bounds the messages of a session sent to the model.
	maxHistory = 40
	// replayToolTurns is how many recent answers are replayed with their
	// tool calls; older answers are replayed as text only.
	replayToolTurns = 3
	// maxReplayedResult bounds a replayed tool result in bytes.
	maxReplayedResult = 8 << 10
	// maxConcurrentAnswers bounds the answers generated at the same time.
	maxConcurrentAnswers = 8
)

// newAssistant returns the client of the configured model, or nil when the
// assistant is disabled.
func newAssistant(cfg *config.AssistantConfig) *assistant.Client {
	if !cfg.Enabled {
		return nil
	}
	return &assistant.Client{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, ReasoningEffort: cfg.ReasoningEffort, HTTP: &http.Client{}}
}

// AssistantInfo is the response of GET /assistant/config.
type AssistantInfo struct {
	Enabled bool     `json:"enabled"`
	Models  []string `json:"models"`
}

func (s *Server) assistantInfo(w http.ResponseWriter, r *http.Request) error {
	info := AssistantInfo{Enabled: s.ai != nil, Models: []string{}}
	if s.ai != nil {
		info.Models = s.cfg.Assistant.Models
	}
	return writeJSON(w, http.StatusOK, info)
}

func (s *Server) requireAssistant() error {
	if s.ai == nil {
		return newError(http.StatusNotFound, "ASSISTANT_DISABLED", "the assistant is not enabled in server.yaml")
	}
	return nil
}

func (s *Server) pathSession(r *http.Request) (*store.AssistantSession, error) {
	if err := s.requireAssistant(); err != nil {
		return nil, err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	sess, err := s.store.AssistantSession(r.Context(), id, currentUser(r).ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("session")
	}
	return sess, err
}

func (s *Server) listAssistantSessions(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAssistant(); err != nil {
		return err
	}
	list, err := s.store.AssistantSessions(r.Context(), currentUser(r).ID, maxAssistantSessions)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, list)
}

type sessionRequest struct {
	Title string `json:"title"`
}

func cleanTitle(t string) string {
	t = strings.Join(strings.Fields(t), " ")
	if utf8.RuneCountInString(t) > 40 {
		t = string([]rune(t)[:40]) + "…"
	}
	return t
}

func (s *Server) createAssistantSession(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAssistant(); err != nil {
		return err
	}
	var req sessionRequest
	if r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			return err
		}
	}
	sess := &store.AssistantSession{UserID: currentUser(r).ID, Title: cleanTitle(req.Title), CreatedAt: s.now()}
	if err := s.store.CreateAssistantSession(r.Context(), sess); err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, sess)
}

// SessionDetail is the response of GET /assistant/sessions/{id}.
type SessionDetail struct {
	store.AssistantSession
	Messages []store.AssistantMessage `json:"messages"`
}

func (s *Server) getAssistantSession(w http.ResponseWriter, r *http.Request) error {
	sess, err := s.pathSession(r)
	if err != nil {
		return err
	}
	msgs, err := s.store.AssistantMessages(r.Context(), sess.ID, 1000)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, SessionDetail{AssistantSession: *sess, Messages: msgs})
}

func (s *Server) renameAssistantSession(w http.ResponseWriter, r *http.Request) error {
	sess, err := s.pathSession(r)
	if err != nil {
		return err
	}
	var req sessionRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if sess.Title = cleanTitle(req.Title); sess.Title == "" {
		return badRequest("title is required")
	}
	if err := s.store.RenameAssistantSession(r.Context(), sess.ID, sess.UserID, sess.Title); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, sess)
}

func (s *Server) deleteAssistantSession(w http.ResponseWriter, r *http.Request) error {
	sess, err := s.pathSession(r)
	if err != nil {
		return err
	}
	if err := s.store.DeleteAssistantSession(r.Context(), sess.ID, sess.UserID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ChatContext is what the user is looking at, so that questions such as
// "how do I restore this backup" can be answered.
type ChatContext struct {
	JobID     string `json:"job_id"`
	HostID    int64  `json:"host_id"`
	ProjectID int64  `json:"project_id"`
}

type chatRequest struct {
	Content string      `json:"content"`
	Model   string      `json:"model"`
	Context ChatContext `json:"context"`
}

// ChatEvent is a server-sent event of POST /assistant/sessions/{id}/messages.
type ChatEvent struct {
	Type       string                  `json:"type"`
	Content    string                  `json:"content,omitzero"`
	ToolCall   *ChatToolCall           `json:"tool_call,omitzero"`
	ToolResult *ChatToolResult         `json:"tool_result,omitzero"`
	Message    *store.AssistantMessage `json:"message,omitzero"`
	Error      string                  `json:"error,omitzero"`
}

// ChatToolCall announces a tool call.
type ChatToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatToolResult is the result of a tool call.
type ChatToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Error   bool   `json:"error,omitzero"`
}

// sseWriter writes JSON server-sent events from several goroutines.
type sseWriter struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	rc     *http.ResponseController
	failed bool
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, rc: http.NewResponseController(w)}
}

func (sw *sseWriter) write(s string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.failed {
		return
	}
	if _, err := fmt.Fprint(sw.w, s); err != nil || sw.rc.Flush() != nil {
		sw.failed = true // the client went away; the answer is still saved
	}
}

func (sw *sseWriter) send(e ChatEvent) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	sw.write("data: " + string(b) + "\n\n")
}

// assistantChat answers a question, streaming the answer as server-sent
// events, and stores the question and the answer.
func (s *Server) assistantChat(w http.ResponseWriter, r *http.Request) error {
	sess, err := s.pathSession(r)
	if err != nil {
		return err
	}
	var req chatRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	req.Content = strings.TrimSpace(req.Content)
	switch n := utf8.RuneCountInString(req.Content); {
	case n == 0:
		return badRequest("content is required")
	case n > maxQuestion:
		return badRequest("the question is longer than %d characters", maxQuestion)
	}
	models := s.cfg.Assistant.Models
	if req.Model == "" {
		req.Model = models[0]
	}
	if !slices.Contains(models, req.Model) {
		return badRequest("model %q is not configured", req.Model)
	}
	if _, busy := s.aiBusy.LoadOrStore(sess.ID, struct{}{}); busy {
		return conflict("an answer is being generated in this session")
	}
	defer s.aiBusy.Delete(sess.ID)
	select {
	case s.aiSlots <- struct{}{}:
		defer func() { <-s.aiSlots }()
	default:
		return newError(http.StatusTooManyRequests, "BUSY", "the assistant is busy; retry later")
	}

	ctx := r.Context()
	// Saving and auditing must finish when the browser goes away.
	keep := r.WithContext(context.WithoutCancel(ctx))
	history, err := s.store.AssistantMessages(ctx, sess.ID, maxHistory)
	if err != nil {
		return err
	}
	u := currentUser(r)
	l := requestLang(r)
	question := &store.AssistantMessage{SessionID: sess.ID, Role: store.RoleUser, Content: req.Content, CreatedAt: s.now()}
	if err := s.store.AddAssistantMessage(ctx, question, cleanTitle(req.Content)); err != nil {
		return err
	}

	sw := newSSEWriter(w)
	sw.send(ChatEvent{Type: "user_message", Message: question})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(sseKeepAlive)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				sw.write(": keep-alive\n\n")
			}
		}
	}()

	input := append(replayHistory(history), assistant.UserMessage(req.Content+s.contextNote(ctx, req.Context)))
	start := time.Now()
	res, runErr := s.ai.Run(ctx, assistant.Request{
		Model: req.Model, Instructions: s.instructions(u, l), Input: input, Tools: s.assistantTools(),
		MaxRounds: s.cfg.Assistant.MaxToolRounds, Timeout: s.cfg.Assistant.Timeout.Std(),
	}, func(e assistant.Event) {
		switch e.Type {
		case assistant.EventContent, assistant.EventReasoning:
			sw.send(ChatEvent{Type: e.Type, Content: e.Text})
		case assistant.EventToolCall:
			sw.send(ChatEvent{Type: e.Type, ToolCall: &ChatToolCall{ID: e.Call.ID, Name: e.Call.Name, Arguments: e.Call.Arguments}})
		case assistant.EventToolResult:
			sw.send(ChatEvent{Type: e.Type, ToolResult: &ChatToolResult{ID: e.Call.ID, Name: e.Call.Name, Content: e.Call.Result, Error: e.Call.Error}})
		}
	})

	answer := &store.AssistantMessage{
		SessionID: sess.ID, Role: store.RoleAssistant, Content: res.Content, Reasoning: res.Reasoning, ToolCalls: []store.AssistantToolCall{},
		Model: req.Model, InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CreatedAt: s.now(),
	}
	tools := make([]string, 0, len(res.ToolCalls))
	for _, c := range res.ToolCalls {
		answer.ToolCalls = append(answer.ToolCalls, store.AssistantToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments, Result: c.Result, Error: c.Error})
		tools = append(tools, c.Name)
	}
	if runErr != nil {
		answer.Error = chatError(runErr, l)
		if !errors.Is(runErr, context.Canceled) {
			s.log.Warn("assistant answer failed", "session", sess.ID, "model", req.Model, "err", runErr)
		}
	}
	if err := s.store.AddAssistantMessage(keep.Context(), answer, ""); err != nil {
		s.log.Error("save assistant answer", "session", sess.ID, "err", err)
		answer.Error = cmp.Or(answer.Error, l.pick("回答未能保存", "The answer could not be saved"))
	}
	s.audit(keep, u, auditAssistantChat, "assistant_session", fmt.Sprint(sess.ID), map[string]any{
		"question": truncateRunes(req.Content, 200), "model": req.Model, "tools": tools,
		"input_tokens": res.InputTokens, "output_tokens": res.OutputTokens,
		"duration_ms": time.Since(start).Milliseconds(), "error": answer.Error,
	})
	if answer.Error != "" {
		sw.send(ChatEvent{Type: "error", Error: answer.Error})
	}
	sw.send(ChatEvent{Type: "done", Message: answer})
	return nil
}

// chatError describes a failed answer to the user in language l.
func chatError(err error, l lang) string {
	ae, isAPI := errors.AsType[*assistant.APIError](err)
	switch {
	case errors.Is(err, context.Canceled):
		return l.pick("已停止生成", "Generation stopped")
	case errors.Is(err, context.DeadlineExceeded):
		return l.pick("模型响应超时，请稍后重试或缩小问题范围", "The model timed out; retry later or narrow the question")
	case isAPI && ae.Status == http.StatusUnauthorized:
		return l.pick("模型服务拒绝了 API key，请检查 server.yaml 中的 assistant 配置",
			"The model service rejected the API key; check the assistant settings in server.yaml")
	case isAPI && ae.Status == http.StatusTooManyRequests:
		return l.pick("模型服务限流或额度不足：", "The model service is rate limited or out of quota: ") + ae.Message
	case isAPI:
		return l.pick("模型服务返回错误：", "The model service returned an error: ") + ae.Message
	}
	return l.pick("调用模型服务失败：", "Calling the model service failed: ") + err.Error()
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// replayHistory converts stored messages to model input. The newest
// answers keep their tool calls so that follow-up questions can refer to
// what was looked up.
func replayHistory(msgs []store.AssistantMessage) []assistant.Item {
	answers := 0
	for _, m := range msgs {
		if m.Role == store.RoleAssistant {
			answers++
		}
	}
	var out []assistant.Item
	seen := 0
	for _, m := range msgs {
		switch m.Role {
		case store.RoleUser:
			out = append(out, assistant.UserMessage(m.Content))
		case store.RoleAssistant:
			seen++
			if answers-seen < replayToolTurns {
				for _, c := range m.ToolCalls {
					result := c.Result
					if len(result) > maxReplayedResult {
						result = strings.ToValidUTF8(result[:maxReplayedResult], "") + "…（已截断）"
					}
					out = append(out, assistant.FunctionCall(c.ID, c.Name, c.Arguments), assistant.FunctionOutput(c.ID, result))
				}
			}
			content := m.Content
			if content == "" && m.Error != "" {
				content = "（上一次回答失败：" + m.Error + "）"
			}
			if content != "" {
				out = append(out, assistant.AssistantMessage(content))
			}
		}
	}
	return out
}

// contextNote describes what the user is looking at.
func (s *Server) contextNote(ctx context.Context, c ChatContext) string {
	var parts []string
	if c.JobID != "" {
		if j, err := s.jobByID(ctx, c.JobID); err == nil {
			parts = append(parts, fmt.Sprintf("备份任务 %s（项目 %s，%s，计划于 %s）", j.ID, j.Project, j.Type, s.aiTime(j.ScheduledAt)))
		}
	}
	if c.HostID != 0 {
		if h, err := s.store.Host(ctx, c.HostID); err == nil {
			parts = append(parts, "主机 "+h.Name)
		}
	}
	if c.ProjectID != 0 {
		if p, err := s.store.Project(ctx, c.ProjectID); err == nil {
			parts = append(parts, fmt.Sprintf("项目 %s（id %d）", p.Name, p.ID))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n（用户正在页面上查看：" + strings.Join(parts, "；") + "。问题中的“这个”通常指它。）"
}

// instructions is the system prompt. It is written in Chinese; only the
// answer language follows the user's UI language l.
func (s *Server) instructions(u *store.User, l lang) string {
	now := s.now().In(s.cfg.Location)
	return fmt.Sprintf(`你是「MySQL 从库备份平台」的只读运维助手，帮助管理员了解备份情况、找到备份文件、制定恢复方案。

当前时间：%s（%s），平台时区 %s。“今天”“本周”等按平台时区理解，一周从周一开始。
当前用户：%s。

平台背景：
- 每台备份主机运行 backup-agent，在 MySQL 从库的 podman 容器内用 xtrabackup 2.4 备份，输出 zstd 压缩的 xbstream 文件 backup.xbstream.zst。
- 一个项目对应一台主机上的一个从库容器。全量备份开启一条备份链，之后的增量接在链上（chained 基于上一个备份，differential 基于全量）。
- 备份文件路径形如 <备份根目录>/<项目>/<YYYYMMDD-HHMM>-chain/<NN>-full 或 <NN>-incr/backup.xbstream.zst；备份根目录在容器内以相同路径挂载，容器内的 MySQL 数据目录是 /var/lib/mysql。
- 任务状态：success 成功，warning 成功但有告警，failed 失败，canceled 已取消，skipped 跳过，pending 排队，running 运行中。

工作方式：
- 你只能通过提供的工具读取数据，不能执行任何修改：不能触发或取消备份、修改配置、暂停调度，也不能在服务器上执行命令。用户要求这类操作时，说明需要由人在平台页面或服务器上操作，可以给出由人执行的步骤。
- 先调用工具获取真实数据再回答，不要编造主机名、路径、时间或数字。用户给出的名字可能不完整或拼写有出入，用 find_projects 或 list_hosts 确认；有多个候选时列出来请用户确认。
- 总结类问题用 backup_summary；问备份文件位置用 latest_backups 或 get_job，给出完整路径以及所在的备份链目录。
- 恢复类问题调用 restore_plan，按它给出的步骤回答，并逐条说明需要用户根据实际情况确认或修改的地方（主库地址和复制账号、目标容器和数据目录、磁盘空间、暂停调度等）。不要省略停止容器、保留原数据目录这类安全步骤；如果用户要恢复到其他主机或目录，说明需要替换的路径和命令。
- %s，先给结论再给细节；多条数据用 Markdown 表格或列表，路径和命令放在代码块中。时间用平台时区。
- 提到具体任务、主机或项目时，附上工具结果中的站内链接，例如 [查看任务](/jobs?id=...)。
- 工具返回错误时，根据错误调整参数重试，或如实告诉用户。`,
		now.Format("2006-01-02 15:04"), weekdays[now.Weekday()], s.cfg.Location, u.Username,
		l.pick("用简体中文回答", "用英文（English）回答（用户界面是英文；即使工具结果和本说明是中文，也要用英文回答）"))
}

var weekdays = [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
