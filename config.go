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
	ReviewLanguage  string   `json:"review_language"`
	ReviewPrompt    string   `json:"review_prompt"`
	MaxDiffLines    int      `json:"max_diff_lines"`
	IgnorePatterns  []string `json:"ignore_patterns"`
	CooldownSeconds int      `json:"cooldown_seconds"`
}

// DefaultReviewPrompt is the default Go template for constructing the review prompt.
const DefaultReviewPrompt = `请审查 GitLab MR #{{.MRIID}}（{{.Title}}）的代码变更。

MR 信息：
- 项目: {{.ProjectName}} (ID: {{.ProjectID}})
- 标题: {{.Title}}
- 描述: {{.Description}}
- 源分支: {{.SourceBranch}} → 目标分支: {{.TargetBranch}}

请执行以下步骤：
1. 使用 gitlab_get_mr_diff 工具获取 MR 的代码变更（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
2. 仔细审查每个文件的变更，关注代码质量、潜在 Bug、安全问题和性能问题
3. 使用 gitlab_post_comment 工具发布整体审查总结（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
4. 对于具体的代码问题，使用 gitlab_post_discussion 工具在对应代码行上添加行内评论（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）

审查语言：{{.ReviewLanguage}}`

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() GitLabPluginConfig {
	return GitLabPluginConfig{
		Listen:          ":9090",
		ReviewLanguage:  "zh-CN",
		ReviewPrompt:    DefaultReviewPrompt,
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
