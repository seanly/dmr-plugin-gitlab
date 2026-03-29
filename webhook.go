package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// ReviewPromptData holds the template variables for the review prompt.
type ReviewPromptData struct {
	ProjectID      int
	ProjectName    string
	MRIID          int
	Title          string
	Description    string
	SourceBranch   string
	TargetBranch   string
	ReviewLanguage string
	// AuthorEmailFromWebhook is set from the webhook JSON when GitLab includes it (last_commit.author.email, then user.email).
	// gitlabGetMrMeta may still return empty author_email if the API token cannot read /users/:id email.
	AuthorEmailFromWebhook string
}

// WebhookServer receives GitLab webhook events and triggers code review.
type WebhookServer struct {
	config     GitLabPluginConfig
	glClient   *GitLabClient
	hostClient *rpc.Client
	dedup      *Deduplicator
	mux        *http.ServeMux
	srv        *http.Server
	mu         sync.Mutex

	// reviewJobQueue is non-nil when max_concurrent_reviews > 0 (worker pool + bounded queue).
	reviewJobQueue chan reviewJob

	mrReviewMu   sync.Mutex
	mrReviewPath string
	mrReviewMod  time.Time
	mrReviewDoc  *mrReviewPromptsFile
}

// reviewJob is one MR review handed to a worker.
type reviewJob struct {
	projectID int
	mrIID     int
	event     GitLabWebhookEvent
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

	if config.MaxConcurrentReviews > 0 {
		q := config.MaxQueuedReviews
		if q <= 0 {
			q = 64
		}
		s.reviewJobQueue = make(chan reviewJob, q)
		for i := 0; i < config.MaxConcurrentReviews; i++ {
			go s.reviewWorker()
		}
		log.Printf("dmr-plugin-gitlab: review limiter active (max_concurrent=%d, max_queued=%d)",
			config.MaxConcurrentReviews, q)
	}

	return s
}

func (s *WebhookServer) reviewWorker() {
	for job := range s.reviewJobQueue {
		s.triggerReview(job.projectID, job.mrIID, job.event)
	}
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
	// Older GitLab versions (< 15.x) don't include "action" in webhook payload;
	// fall back to inferring from "state".
	action := event.ObjectAttributes.Action
	if action == "" {
		switch event.ObjectAttributes.State {
		case "opened":
			action = "open"
		case "merged", "closed":
			// not actionable
		}
	}
	if action != "open" && action != "update" && action != "reopen" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored", "reason": "action: " + action})
		return
	}

	projectID := event.Project.ID
	mrIID := event.ObjectAttributes.IID

	// Dedup check (reserves slot; released if queue is full — see Forget below)
	if !s.dedup.ShouldProcess(projectID, mrIID) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "reason": "cooldown"})
		return
	}

	if s.reviewJobQueue != nil {
		job := reviewJob{projectID: projectID, mrIID: mrIID, event: event}
		select {
		case s.reviewJobQueue <- job:
			// queued for a worker
		default:
			s.dedup.Forget(projectID, mrIID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "rejected", "reason": "review queue full (max concurrent + buffer exceeded)",
			})
			return
		}
	} else {
		go s.triggerReview(projectID, mrIID, event)
	}

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

	data := ReviewPromptData{
		ProjectID:              projectID,
		ProjectName:            event.Project.Name,
		MRIID:                  mrIID,
		Title:                  event.ObjectAttributes.Title,
		Description:            event.ObjectAttributes.Description,
		SourceBranch:           event.ObjectAttributes.SourceBranch,
		TargetBranch:           event.ObjectAttributes.TargetBranch,
		ReviewLanguage:         s.config.ReviewLanguage,
		AuthorEmailFromWebhook: authorEmailFromWebhook(event),
	}

	tmplStr := s.resolveReviewTemplate(event.Project.PathWithNamespace)

	prompt, err := renderPrompt(tmplStr, data)
	if err != nil {
		log.Printf("dmr-plugin-gitlab: render prompt error: %v, falling back to default", err)
		prompt, _ = renderPrompt(DefaultReviewPrompt, data)
	}

	req := &proto.RunAgentRequest{
		TapeName: tapeName,
		Prompt:   prompt,
	}
	var resp proto.RunAgentResponse

	cleanupMergeBlock := s.prepareMergeBlockDuringReview(projectID, mrIID, event.ObjectAttributes.WorkInProgress)
	defer cleanupMergeBlock()

	log.Printf("dmr-plugin-gitlab: triggering review for %s", tapeName)
	if err := host.Call("Plugin.RunAgent", req, &resp); err != nil {
		log.Printf("dmr-plugin-gitlab: RunAgent RPC error: %v", err)
		return
	}
	if resp.Error != "" {
		log.Printf("dmr-plugin-gitlab: RunAgent failed: %s", resp.Error)
		return
	}
	log.Printf("dmr-plugin-gitlab: review completed for %s (%d steps)", tapeName, resp.Steps)
}

// prepareMergeBlockDuringReview marks the MR as non-mergeable while review runs (GitLab 13.8+).
// Uses work_in_progress API first; if that fails, falls back to a "WIP: " title prefix.
// If the MR is already WIP or blocking fails, returns a no-op cleanup.
func (s *WebhookServer) prepareMergeBlockDuringReview(projectID, mrIID int, alreadyWIP bool) (cleanup func()) {
	nop := func() {}
	if !s.config.BlockMergeDuringReview {
		return nop
	}
	if alreadyWIP {
		return nop
	}

	// Prefer API flag (same as UI “进行中” / Draft).
	if err := s.glClient.SetMRWorkInProgress(projectID, mrIID, true); err == nil {
		log.Printf("dmr-plugin-gitlab: MR %d/%d marked work_in_progress during review", projectID, mrIID)
		return func() {
			if e := s.glClient.SetMRWorkInProgress(projectID, mrIID, false); e != nil {
				log.Printf("dmr-plugin-gitlab: clear work_in_progress for %d/%d: %v", projectID, mrIID, e)
			}
		}
	} else {
		log.Printf("dmr-plugin-gitlab: work_in_progress API failed, trying WIP title: %v", err)
	}

	title, err := s.glClient.GetMRTitle(projectID, mrIID)
	if err != nil {
		log.Printf("dmr-plugin-gitlab: merge block skipped (get title): %v", err)
		return nop
	}
	if mergeBlockTitlePrefix(title) {
		return nop
	}
	newTitle := "WIP: " + title
	if err := s.glClient.UpdateMergeRequest(projectID, mrIID, map[string]any{"title": newTitle}); err != nil {
		log.Printf("dmr-plugin-gitlab: merge block skipped (title): %v", err)
		return nop
	}
	log.Printf("dmr-plugin-gitlab: MR %d/%d title prefixed with WIP during review", projectID, mrIID)
	return func() {
		if e := s.glClient.UpdateMergeRequest(projectID, mrIID, map[string]any{"title": title}); e != nil {
			log.Printf("dmr-plugin-gitlab: restore MR title for %d/%d: %v", projectID, mrIID, e)
		}
	}
}

func mergeBlockTitlePrefix(title string) bool {
	t := strings.TrimSpace(strings.ToUpper(title))
	return strings.HasPrefix(t, "WIP:") || strings.HasPrefix(t, "WIP ") ||
		strings.HasPrefix(t, "DRAFT:") || strings.HasPrefix(t, "DRAFT ")
}

// renderPrompt renders the review prompt template with the given data.
func renderPrompt(tmplStr string, data ReviewPromptData) (string, error) {
	tmpl, err := template.New("review").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
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
	ObjectKind       string        `json:"object_kind"`
	User             *WebhookUser  `json:"user"`
	Project          GitLabProject `json:"project"`
	ObjectAttributes MRAttributes  `json:"object_attributes"`
}

// WebhookUser is the "user" who triggered the hook (often the MR author on open).
type WebhookUser struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type GitLabProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
}

type MRAttributes struct {
	IID            int         `json:"iid"`
	Title          string      `json:"title"`
	Description    string      `json:"description"`
	Action         string      `json:"action"`
	State          string      `json:"state"`
	SourceBranch   string      `json:"source_branch"`
	TargetBranch   string      `json:"target_branch"`
	WorkInProgress bool        `json:"work_in_progress"`
	LastCommit     *LastCommit `json:"last_commit"`
}

// LastCommit mirrors object_attributes.last_commit on merge_request webhooks.
type LastCommit struct {
	ID      string       `json:"id"`
	Message string       `json:"message"`
	Author  CommitPerson `json:"author"`
}

// CommitPerson is name + email as GitLab sends on commits.
type CommitPerson struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func authorEmailFromWebhook(event GitLabWebhookEvent) string {
	if lc := event.ObjectAttributes.LastCommit; lc != nil {
		if e := strings.TrimSpace(lc.Author.Email); e != "" {
			return e
		}
	}
	if event.User != nil {
		if e := strings.TrimSpace(event.User.Email); e != "" {
			return e
		}
	}
	return ""
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

// Forget removes the cooldown entry (e.g. when a review was not actually queued).
func (d *Deduplicator) Forget(projectID, mrIID int) {
	key := fmt.Sprintf("%d:%d", projectID, mrIID)
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, key)
}
