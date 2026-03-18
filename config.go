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
	MaxDiffLines    int      `json:"max_diff_lines"`
	IgnorePatterns  []string `json:"ignore_patterns"`
	CooldownSeconds int      `json:"cooldown_seconds"`
}

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
