package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"sync"
	"time"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// WebhookServer receives GitLab webhook events and triggers code review.
type WebhookServer struct {
	config     GitLabPluginConfig
	glClient   *GitLabClient
	hostClient *rpc.Client
	dedup      *Deduplicator
	mux        *http.ServeMux
	srv        *http.Server
	mu         sync.Mutex
}

// NewWebhookServer creates a new webhook HTTP server.
func NewWebhookServer(config GitLabPluginConfig, glClient *GitLabClient, hostClient *rpc.Client) *WebhookServer {
	s := &WebhookServer{
		config:     config,
		glClient:   glClient,
		hostClient: hostClient,
		dedup:      NewDeduplicator(time.Duration(config.CooldownSeconds) * time.Second),
		mux:        http.NewServeMux(),
	}
	s.mux.HandleFunc("/webhook/gitlab", s.handleWebhook)
	s.mux.HandleFunc("/health", s.handleHealth)
	return s
}

// SetHostClient sets the reverse RPC client (called after broker connection).
func (s *WebhookServer) SetHostClient(c *rpc.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostClient = c
}

// Start starts the HTTP server.
func (s *WebhookServer) Start() error {
	s.srv = &http.Server{
		Addr:    s.config.Listen,
		Handler: s.mux,
	}
	ln, err := net.Listen("tcp", s.config.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.config.Listen, err)
	}
	log.Printf("dmr-plugin-gitlab: webhook server at http://%s/webhook/gitlab", ln.Addr())
	return s.srv.Serve(ln)
}

// Stop gracefully stops the HTTP server.
func (s *WebhookServer) Stop() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// handleWebhook processes incoming GitLab webhook events.
func (s *WebhookServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify secret token
	if s.config.WebhookSecret != "" {
		token := r.Header.Get("X-Gitlab-Token")
		if token != s.config.WebhookSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Parse event
	var event GitLabWebhookEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Only handle merge_request events
	if event.ObjectKind != "merge_request" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored", "reason": "not a merge_request event"})
		return
	}

	// Only handle open/update actions
	action := event.ObjectAttributes.Action
	if action != "open" && action != "update" && action != "reopen" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored", "reason": "action: " + action})
		return
	}

	projectID := event.Project.ID
	mrIID := event.ObjectAttributes.IID

	// Dedup check
	if !s.dedup.ShouldProcess(projectID, mrIID) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "reason": "cooldown"})
		return
	}

	// Trigger review asynchronously
	go s.triggerReview(projectID, mrIID, event)

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// triggerReview calls the DMR host's RunAgent to perform code review.
func (s *WebhookServer) triggerReview(projectID, mrIID int, event GitLabWebhookEvent) {
	s.mu.Lock()
	host := s.hostClient
	s.mu.Unlock()

	if host == nil {
		log.Printf("dmr-plugin-gitlab: host client not ready, skipping review for %d:%d", projectID, mrIID)
		return
	}

	tapeName := fmt.Sprintf("gitlab:%d:mr:%d", projectID, mrIID)

	prompt := fmt.Sprintf(`请审查 GitLab MR #%d（%s）的代码变更。

MR 信息：
- 项目: %s (ID: %d)
- 标题: %s
- 描述: %s
- 源分支: %s → 目标分支: %s

请执行以下步骤：
1. 使用 gitlab_get_mr_diff 工具获取 MR 的代码变更（project_id=%d, mr_iid=%d）
2. 仔细审查每个文件的变更，关注代码质量、潜在 Bug、安全问题和性能问题
3. 使用 gitlab_post_comment 工具发布整体审查总结（project_id=%d, mr_iid=%d）
4. 对于具体的代码问题，使用 gitlab_post_discussion 工具在对应代码行上添加行内评论（project_id=%d, mr_iid=%d）

审查语言：%s`,
		mrIID, event.ObjectAttributes.Title,
		event.Project.Name, projectID,
		event.ObjectAttributes.Title,
		event.ObjectAttributes.Description,
		event.ObjectAttributes.SourceBranch, event.ObjectAttributes.TargetBranch,
		projectID, mrIID,
		projectID, mrIID,
		projectID, mrIID,
		s.config.ReviewLanguage,
	)

	req := &proto.RunAgentRequest{
		TapeName: tapeName,
		Prompt:   prompt,
	}
	var resp proto.RunAgentResponse

	log.Printf("dmr-plugin-gitlab: triggering review for %s", tapeName)
	if err := host.Call("HostService.RunAgent", req, &resp); err != nil {
		log.Printf("dmr-plugin-gitlab: RunAgent RPC error: %v", err)
		return
	}
	if resp.Error != "" {
		log.Printf("dmr-plugin-gitlab: RunAgent failed: %s", resp.Error)
		return
	}
	log.Printf("dmr-plugin-gitlab: review completed for %s (%d steps)", tapeName, resp.Steps)
}

func (s *WebhookServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"plugin": "dmr-plugin-gitlab",
	})
}

// --- GitLab Webhook Event Types ---

// GitLabWebhookEvent represents a GitLab webhook payload.
type GitLabWebhookEvent struct {
	ObjectKind       string           `json:"object_kind"`
	Project          GitLabProject    `json:"project"`
	ObjectAttributes MRAttributes     `json:"object_attributes"`
}

type GitLabProject struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type MRAttributes struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Action       string `json:"action"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
}

// --- Deduplicator ---

// Deduplicator prevents processing the same MR multiple times within a cooldown period.
type Deduplicator struct {
	mu       sync.Mutex
	seen     map[string]time.Time
	cooldown time.Duration
}

func NewDeduplicator(cooldown time.Duration) *Deduplicator {
	return &Deduplicator{
		seen:     make(map[string]time.Time),
		cooldown: cooldown,
	}
}

func (d *Deduplicator) ShouldProcess(projectID, mrIID int) bool {
	key := fmt.Sprintf("%d:%d", projectID, mrIID)
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.seen[key]; ok && time.Since(last) < d.cooldown {
		return false
	}
	d.seen[key] = time.Now()
	return true
}
