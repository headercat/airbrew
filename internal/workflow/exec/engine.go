// Package exec walks validated workflow graphs and persists run timelines.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/logging"
	"github.com/headercat/airbrew/internal/workflow/defn"
	"github.com/headercat/airbrew/internal/workflow/run"
)

const (
	defaultRunTimeout = 60 * time.Second
	maxRunTimeout     = 10 * time.Minute
	maxDelay          = 5 * time.Minute
	maxHTTPBody       = 64 * 1024
)

// MailSender is implemented by module wiring that can deliver workflow mail.
type MailSender interface {
	SendWorkflowMail(ctx context.Context, userID, mailboxID, to, subject, body string) error
}

// Engine executes workflows.
type Engine struct {
	svc    *run.Service
	client *http.Client
	mail   MailSender
}

// New returns an Engine bound to the workflow service.
func New(svc *run.Service, mail MailSender) *Engine {
	return &Engine{
		svc: svc,
		client: &http.Client{
			Timeout: 20 * time.Second,
			// Re-validate every redirect target: isPrivateHost only checks the
			// user-supplied URL, so without this a malicious endpoint could
			// 302 to http://169.254.169.254/... and bypass the SSRF guard.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("workflow: too many redirects")
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return errors.New("workflow: redirect to non-http scheme blocked")
				}
				if isPrivateHost(req.URL.Hostname()) {
					return errors.New("workflow: redirect to a private host blocked")
				}
				return nil
			},
		},
		mail: mail,
	}
}

// Request describes one workflow execution.
type Request struct {
	Workflow *run.Workflow
	Trigger  run.RunTrigger
	Input    map[string]any
	Timeout  time.Duration
}

// Execute runs the workflow synchronously and returns the persisted run row.
func (e *Engine) Execute(ctx context.Context, req Request) (*run.Run, error) {
	if req.Workflow == nil {
		return nil, errors.New("workflow: workflow is required")
	}
	if err := req.Workflow.Definition.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", run.ErrDefinitionInvalid, err)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultRunTimeout
	}
	if timeout > maxRunTimeout {
		timeout = maxRunTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	inputJSON := mustJSON(req.Input)
	rn := &run.Run{
		WorkflowID: req.Workflow.ID,
		UserID:     req.Workflow.UserID,
		Version:    req.Workflow.Version,
		Status:     run.RunRunning,
		Trigger:    req.Trigger,
		InputJSON:  inputJSON,
	}
	if rn.Trigger == "" {
		rn.Trigger = run.RunByManual
	}
	if err := e.svc.Repo().CreateRun(ctx, rn); err != nil {
		return nil, err
	}

	state := &runState{
		runID:   rn.ID,
		trigger: copyMap(req.Input),
		vars:    map[string]any{},
	}
	err := e.walk(ctx, req.Workflow, state, req.Input)
	status := run.RunSuccess
	errMsg := ""
	if err != nil {
		status = run.RunFailed
		errMsg = err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			status = run.RunTimedOut
		}
		if errors.Is(err, errCancelled) {
			status = run.RunCancelled
			errMsg = "cancelled"
		}
		// A context.Cancellation that is not the internal cancel sentinel is
		// a shutdown / lifecycle cancellation (scheduler ctx, request ctx on
		// the async path); record it as cancelled rather than "failed".
		if errors.Is(err, context.Canceled) {
			status = run.RunCancelled
			errMsg = "cancelled"
		}
	}
	if ferr := e.svc.Repo().FinishRun(context.Background(), rn.ID, status, errMsg); ferr != nil && err == nil {
		err = ferr
	}
	if e.svc.Audit() != nil {
		e.svc.Audit().Log(context.Background(), audit.Entry{
			EventType: "workflow.run." + string(status), ActorUserID: req.Workflow.UserID,
			TargetType: "workflow_run", TargetID: rn.ID,
			Metadata: map[string]any{"workflow_id": req.Workflow.ID, "trigger": rn.Trigger},
		})
	}
	refreshed, gerr := e.svc.Repo().GetRunByID(context.Background(), rn.ID)
	if gerr == nil {
		rn = refreshed
	}
	return rn, err
}

// ExecuteAsync creates the run row synchronously (so the caller gets a run id
// immediately, with status "running") and walks the graph in a background
// goroutine detached from the request context. It is intended for webhook
// triggers, whose HTTP senders routinely time out before a synchronous run
// finishes — blocking the response would make them retry and fire duplicate
// runs. The returned run is in "running" state; the caller should poll the run
// id or subscribe to run history for the final status.
func (e *Engine) ExecuteAsync(ctx context.Context, req Request) (*run.Run, error) {
	if req.Workflow == nil {
		return nil, errors.New("workflow: workflow is required")
	}
	if err := req.Workflow.Definition.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", run.ErrDefinitionInvalid, err)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultRunTimeout
	}
	if timeout > maxRunTimeout {
		timeout = maxRunTimeout
	}
	rn := &run.Run{
		WorkflowID: req.Workflow.ID,
		UserID:     req.Workflow.UserID,
		Version:    req.Workflow.Version,
		Status:     run.RunRunning,
		Trigger:    req.Trigger,
		InputJSON:  mustJSON(req.Input),
	}
	if rn.Trigger == "" {
		rn.Trigger = run.RunByManual
	}
	// Create the run row on the caller's ctx so a fast-fail (bad workflow,
	// DB down) is reported to the caller rather than swallowed.
	if err := e.svc.Repo().CreateRun(ctx, rn); err != nil {
		return nil, err
	}
	wf := req.Workflow
	input := req.Input
	logging.Go("workflow.runDetached", func() { e.runDetached(wf, input, rn, timeout) })
	return rn, nil
}

// runDetached walks the graph on a background context (independent of any
// HTTP request) and persists the final status. It must not reference the
// caller's ctx: webhook senders disconnect the moment they receive 202.
func (e *Engine) runDetached(wf *run.Workflow, input map[string]any, rn *run.Run, timeout time.Duration) {
	// Recover from a panic so the run row is never left stuck in "running"
	// until the next process restart; ReapStaleRunning would only catch it
	// on the next startup, leaving the SPA's auto-poll spinning in the
	// meantime.
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(context.Background(), "workflow: run panicked",
				"run_id", rn.ID, "workflow_id", wf.ID, "panic", r)
			_ = e.svc.Repo().FinishRun(context.Background(), rn.ID, run.RunFailed, fmt.Sprintf("panic: %v", r))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	state := &runState{
		runID:   rn.ID,
		trigger: copyMap(input),
		vars:    map[string]any{},
	}
	err := e.walk(ctx, wf, state, input)
	status := run.RunSuccess
	errMsg := ""
	if err != nil {
		status = run.RunFailed
		errMsg = err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			status = run.RunTimedOut
		}
		if errors.Is(err, errCancelled) || errors.Is(err, context.Canceled) {
			status = run.RunCancelled
			errMsg = "cancelled"
		}
	}
	_ = e.svc.Repo().FinishRun(context.Background(), rn.ID, status, errMsg)
	if e.svc.Audit() != nil {
		e.svc.Audit().Log(context.Background(), audit.Entry{
			EventType: "workflow.run." + string(status), ActorUserID: wf.UserID,
			TargetType: "workflow_run", TargetID: rn.ID,
			Metadata: map[string]any{"workflow_id": wf.ID, "trigger": rn.Trigger},
		})
	}
}

type queuedNode struct {
	node  *defn.Node
	input map[string]any
}

type runState struct {
	runID   string
	trigger map[string]any
	vars    map[string]any
	seq     int
}

var errCancelled = errors.New("workflow: cancelled")

func (e *Engine) walk(ctx context.Context, w *run.Workflow, st *runState, input map[string]any) error {
	trigger := w.Definition.TriggerNode()
	queue := []queuedNode{{node: trigger, input: input}}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		cancelled, err := e.svc.IsCancelled(context.Background(), st.runID)
		if err != nil {
			return err
		}
		if cancelled {
			return errCancelled
		}
		item := queue[0]
		queue = queue[1:]
		out, port, err := e.executeNode(ctx, w.UserID, st, item.node, item.input)
		if err != nil {
			return err
		}
		for _, next := range w.Definition.NextNodes(item.node.ID, port) {
			queue = append(queue, queuedNode{node: next, input: out})
		}
	}
	return nil
}

func (e *Engine) executeNode(ctx context.Context, userID string, st *runState, n *defn.Node, input map[string]any) (map[string]any, string, error) {
	st.seq++
	step := &run.StepRun{
		RunID: st.runID, NodeID: n.ID, NodeType: n.Type,
		Status: run.StepRunning, InputJSON: mustJSON(input), OutputJSON: "{}",
		Seq: st.seq,
	}
	start := time.Now()
	if err := e.svc.Repo().CreateStep(ctx, step); err != nil {
		return nil, "", err
	}
	out, port, err := e.runNode(ctx, userID, st, n, input)
	status := run.StepSuccess
	errMsg := ""
	if err != nil {
		status = run.StepFailed
		errMsg = err.Error()
	}
	if out == nil {
		out = map[string]any{}
	}
	ferr := e.svc.Repo().FinishStep(context.Background(), step.ID, status, mustJSON(out), errMsg, time.Since(start).Milliseconds())
	if err != nil {
		// Surface a FinishStep failure even when the node itself failed,
		// otherwise a DB hiccup here would leave the step row stuck in
		// "running" with no log trail.
		if ferr != nil {
			slog.WarnContext(ctx, "workflow: finish step failed",
				"run_id", st.runID, "node_id", n.ID, "error", ferr)
		}
		return nil, "", err
	}
	if ferr != nil {
		return nil, "", ferr
	}
	return out, port, nil
}

func (e *Engine) runNode(ctx context.Context, userID string, st *runState, n *defn.Node, input map[string]any) (map[string]any, string, error) {
	env := env{json: input, trigger: st.trigger, vars: st.vars}
	switch n.Type {
	case "trigger.manual", "trigger.webhook", "trigger.schedule":
		return input, "out", nil
	case "logic.if":
		var cfg struct {
			Expr string `json:"expr"`
		}
		if err := decodeConfig(n, &cfg); err != nil {
			return nil, "", err
		}
		if evalCondition(cfg.Expr, env) {
			return input, "true", nil
		}
		return input, "false", nil
	case "logic.delay":
		var cfg struct {
			Seconds int `json:"seconds"`
		}
		if err := decodeConfig(n, &cfg); err != nil {
			return nil, "", err
		}
		d := time.Duration(cfg.Seconds) * time.Second
		if d < 0 || d > maxDelay {
			return nil, "", fmt.Errorf("workflow: delay seconds must be between 0 and %d", int(maxDelay.Seconds()))
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-timer.C:
			return input, "out", nil
		}
	case "logic.setvar":
		var cfg struct {
			Vars map[string]any `json:"vars"`
		}
		if err := decodeConfig(n, &cfg); err != nil {
			return nil, "", err
		}
		for k, v := range cfg.Vars {
			st.vars[k] = renderAny(v, env)
		}
		out := copyMap(input)
		out["vars"] = copyMap(st.vars)
		return out, "out", nil
	case "action.log":
		var cfg struct {
			Message string `json:"message"`
		}
		if err := decodeConfig(n, &cfg); err != nil {
			return nil, "", err
		}
		out := copyMap(input)
		out["message"] = render(cfg.Message, env)
		return out, "out", nil
	case "action.http":
		return e.httpAction(ctx, n, env)
	case "action.mail":
		var cfg struct {
			MailboxID string `json:"mailbox_id"`
			To        string `json:"to"`
			Subject   string `json:"subject"`
			Body      string `json:"body"`
		}
		if err := decodeConfig(n, &cfg); err != nil {
			return nil, "", err
		}
		if e.mail == nil {
			return nil, "", errors.New("workflow: mail sender is not configured")
		}
		to := render(cfg.To, env)
		subject := render(cfg.Subject, env)
		body := render(cfg.Body, env)
		mailboxID := render(cfg.MailboxID, env)
		if mailboxID == "" {
			return nil, "", errors.New("workflow: mail action requires mailbox_id")
		}
		if err := e.mail.SendWorkflowMail(ctx, userID, mailboxID, to, subject, body); err != nil {
			return nil, "", err
		}
		return map[string]any{"sent": true, "to": to}, "out", nil
	default:
		return nil, "", fmt.Errorf("workflow: unsupported node type %q", n.Type)
	}
}

func (e *Engine) httpAction(ctx context.Context, n *defn.Node, env env) (map[string]any, string, error) {
	var cfg struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    any               `json:"body"`
		AllowPrivate bool         `json:"allow_private"`
	}
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	method := strings.ToUpper(strings.TrimSpace(render(cfg.Method, env)))
	if method == "" {
		method = http.MethodGet
	}
	rawURL := render(cfg.URL, env)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("workflow: invalid http url")
	}
	if !cfg.AllowPrivate && isPrivateHost(u.Hostname()) {
		return nil, "", fmt.Errorf("workflow: private http hosts require allow_private")
	}
	var body io.Reader
	if cfg.Body != nil {
		b, err := json.Marshal(renderAny(cfg.Body, env))
		if err != nil {
			return nil, "", err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, render(v, env))
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil {
		return nil, "", err
	}
	truncated := len(data) > maxHTTPBody
	if truncated {
		data = data[:maxHTTPBody]
	}
	out := map[string]any{
		"status":     resp.StatusCode,
		"headers":    resp.Header,
		"body":       string(data),
		"truncated":  truncated,
		"successful": resp.StatusCode >= 200 && resp.StatusCode < 300,
	}
	var parsed any
	if json.Unmarshal(data, &parsed) == nil {
		out["json"] = parsed
	}
	return out, "out", nil
}

func isPrivateHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.LookupIP(host)
		if err != nil {
			return true
		}
		for _, item := range ips {
			if isPrivateIP(item) {
				return true
			}
		}
		return false
	}
	return isPrivateIP(ip)
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func decodeConfig(n *defn.Node, v any) error {
	if len(n.Config) == 0 {
		return nil
	}
	if err := json.Unmarshal(n.Config, v); err != nil {
		return fmt.Errorf("workflow: node %s config: %w", n.ID, err)
	}
	return nil
}

type env struct {
	json    map[string]any
	trigger map[string]any
	vars    map[string]any
}

var templateRe = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

func render(s string, e env) string {
	return templateRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := templateRe.FindStringSubmatch(m)
		if len(sub) != 2 {
			return ""
		}
		v, _ := resolveExpr(strings.TrimSpace(sub[1]), e)
		switch x := v.(type) {
		case string:
			return x
		case nil:
			return ""
		default:
			b, _ := json.Marshal(x)
			return string(b)
		}
	})
}

func renderAny(v any, e env) any {
	switch x := v.(type) {
	case string:
		return render(x, e)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = renderAny(x[i], e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = renderAny(v, e)
		}
		return out
	default:
		return v
	}
}

func evalCondition(expr string, e env) bool {
	expr = strings.TrimSpace(render(expr, e))
	if expr == "" {
		return false
	}
	fields := strings.Fields(expr)
	if len(fields) >= 3 {
		left := strings.Join(fields[:len(fields)-2], " ")
		op := fields[len(fields)-2]
		right := fields[len(fields)-1]
		switch op {
		case "eq", "==":
			return left == trimQuotes(right)
		case "ne", "!=":
			return left != trimQuotes(right)
		case "gt", ">":
			return compareFloat(left, right) > 0
		case "gte", ">=":
			return compareFloat(left, right) >= 0
		case "lt", "<":
			return compareFloat(left, right) < 0
		case "lte", "<=":
			return compareFloat(left, right) <= 0
		}
	}
	switch strings.ToLower(expr) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func compareFloat(a, b string) int {
	af, _ := strconv.ParseFloat(trimQuotes(a), 64)
	bf, _ := strconv.ParseFloat(trimQuotes(b), 64)
	switch {
	case af > bf:
		return 1
	case af < bf:
		return -1
	default:
		return 0
	}
}

func trimQuotes(s string) string {
	return strings.Trim(strings.TrimSpace(s), `"'`)
}

func resolveExpr(expr string, e env) (any, bool) {
	switch {
	case strings.HasPrefix(expr, "$json"):
		return resolvePath(e.json, strings.TrimPrefix(expr, "$json"))
	case strings.HasPrefix(expr, "$trigger"):
		return resolvePath(e.trigger, strings.TrimPrefix(expr, "$trigger"))
	case strings.HasPrefix(expr, "$vars"):
		return resolvePath(e.vars, strings.TrimPrefix(expr, "$vars"))
	default:
		return expr, true
	}
}

func resolvePath(root any, path string) (any, bool) {
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return root, true
	}
	cur := root
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func mustJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func copyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
