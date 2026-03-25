package main

// GitLabPluginConfig holds the plugin configuration.
type GitLabPluginConfig struct {
	// Webhook server
	Listen        string `json:"listen"`
	WebhookSecret string `json:"webhook_secret"`

	// GitLab API
	GitLabURL   string `json:"gitlab_url"`
	GitLabToken string `json:"gitlab_token"`

	// Review behavior
	ReviewLanguage string `json:"review_language"`
	// ReviewPromptSource: builtin | config | current | external.
	// Omit or leave empty: legacy — if review_prompt_file set → current; else if review_prompt set → config; else builtin.
	// Explicit unknown value: logged and treated as builtin.
	ReviewPromptSource      string   `json:"review_prompt_source"`
	ReviewPrompt            string   `json:"review_prompt"`
	ReviewPromptFile        string   `json:"review_prompt_file"`
	ReviewPromptRef         string   `json:"review_prompt_ref"`          // branch/tag/SHA; empty → project default_branch (for current/external)
	ReviewPromptProjectPath string   `json:"review_prompt_project_path"` // GitLab namespaced path, e.g. group/sub/repo (external only)
	ConfigBaseDir           string   `json:"config_base_dir"`            // injected by DMR; base for relative review_prompt file paths (config mode)
	MaxDiffLines            int      `json:"max_diff_lines"`
	IgnorePatterns          []string `json:"ignore_patterns"`
	CooldownSeconds         int      `json:"cooldown_seconds"`
	BlockMergeDuringReview  bool     `json:"block_merge_during_review"` // mark MR WIP while DMR runs (GitLab blocks merge)

	// Concurrency: 0 = unlimited (each webhook spawns its own goroutine, legacy behavior).
	// When MaxConcurrentReviews > 0, jobs go through a worker pool; queue full → HTTP 503.
	MaxConcurrentReviews int `json:"max_concurrent_reviews"`
	MaxQueuedReviews     int `json:"max_queued_reviews"` // buffered queue length; if 0 while concurrent>0, defaults to 64
}

// DefaultReviewPrompt is the default Go template for constructing the review prompt.
const DefaultReviewPrompt = `请审查 GitLab MR #{{.MRIID}}（{{.Title}}）的代码变更。

MR 信息：
- 项目: {{.ProjectName}} (ID: {{.ProjectID}})
- 标题: {{.Title}}
- 描述: {{.Description}}
- 源分支: {{.SourceBranch}} → 目标分支: {{.TargetBranch}}

请执行以下步骤：
1. 使用 gitlabGetMrDiff 工具获取 MR 的代码变更（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
2. 仔细审查每个文件的变更，关注代码质量、潜在 Bug、安全问题和性能问题
3. 使用 gitlabPostComment 工具发布整体审查总结（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
4. 对于具体的代码问题，使用 gitlabPostDiscussion 工具在对应代码行上添加行内评论（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
5. 在完成 comment/discussion 之后，调用 tape.handoff 工具：为当前 tape 添加一个 phase boundary anchor，避免下一次审查把本次历史全部带入上下文。
   - name 建议：gitlab:mr:{{.MRIID}}:review-done:<UTC时间戳>
     （UTC 时间戳格式建议：20060102-150405，例如 20260320-181530。）
   - summary 可选：review completed for mr_iid={{.MRIID}} at <UTC时间戳>

审查语言：{{.ReviewLanguage}}`

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() GitLabPluginConfig {
	return GitLabPluginConfig{
		Listen:          ":9090",
		ReviewLanguage:  "zh-CN",
		MaxDiffLines:    2000,
		CooldownSeconds: 30,
		IgnorePatterns: []string{
			"*.lock",
			"*.min.js",
			"*.min.css",
			"vendor/**",
			"go.sum",
		},
	}
}
