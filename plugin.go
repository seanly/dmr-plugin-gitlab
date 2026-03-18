package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/rpc"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// GitLabPlugin implements proto.DMRPluginInterface.
// It receives GitLab MR webhooks and triggers code review via DMR agent loop.
type GitLabPlugin struct {
	config     GitLabPluginConfig
	server     *WebhookServer
	glClient   *GitLabClient
	hostClient *rpc.Client // reverse RPC client to host (set via SetHostClient)
}

// NewGitLabPlugin creates a new GitLabPlugin with default config.
func NewGitLabPlugin() *GitLabPlugin {
	return &GitLabPlugin{
		config: DefaultConfig(),
	}
}

func (p *GitLabPlugin) Init(req *proto.InitRequest, resp *proto.InitResponse) error {
	if req.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(req.ConfigJSON), &p.config); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}

	// Initialize GitLab API client
	if p.config.GitLabURL == "" || p.config.GitLabToken == "" {
		return fmt.Errorf("gitlab_url and gitlab_token are required")
	}
	p.glClient = NewGitLabClient(p.config.GitLabURL, p.config.GitLabToken)

	// Start webhook HTTP server
	p.server = NewWebhookServer(p.config, p.glClient, p.hostClient)
	go func() {
		log.Printf("dmr-plugin-gitlab: starting webhook server on %s", p.config.Listen)
		if err := p.server.Start(); err != nil {
			log.Printf("dmr-plugin-gitlab: webhook server error: %v", err)
		}
	}()

	return nil
}

// SetHostClient implements proto.HostClientSetter.
func (p *GitLabPlugin) SetHostClient(c *rpc.Client) {
	p.hostClient = c
	if p.server != nil {
		p.server.SetHostClient(c)
	}
}

func (p *GitLabPlugin) Shutdown(req *proto.ShutdownRequest, resp *proto.ShutdownResponse) error {
	if p.server != nil {
		return p.server.Stop()
	}
	return nil
}

func (p *GitLabPlugin) RequestApproval(req *proto.ApprovalRequest, resp *proto.ApprovalResult) error {
	// GitLab plugin does not provide approval — return denied
	resp.Choice = 0
	resp.Comment = "gitlab plugin does not handle approvals"
	return nil
}

func (p *GitLabPlugin) RequestBatchApproval(req *proto.BatchApprovalRequest, resp *proto.BatchApprovalResult) error {
	resp.Choice = 0
	return nil
}

// ProvideTools returns the tools this plugin provides to the DMR agent.
func (p *GitLabPlugin) ProvideTools(req *proto.ProvideToolsRequest, resp *proto.ProvideToolsResponse) error {
	resp.Tools = []proto.ToolDef{
		{
			Name:        "gitlab_get_mr_diff",
			Description: "获取 GitLab MR 的代码变更 diff",
			ParametersJSON: `{
				"type": "object",
				"properties": {
					"project_id": {"type": "integer", "description": "GitLab project ID"},
					"mr_iid": {"type": "integer", "description": "Merge Request IID"}
				},
				"required": ["project_id", "mr_iid"]
			}`,
		},
		{
			Name:        "gitlab_post_comment",
			Description: "在 GitLab MR 上发布评论（整体审查总结）",
			ParametersJSON: `{
				"type": "object",
				"properties": {
					"project_id": {"type": "integer", "description": "GitLab project ID"},
					"mr_iid": {"type": "integer", "description": "Merge Request IID"},
					"body": {"type": "string", "description": "Markdown 格式的评论内容"}
				},
				"required": ["project_id", "mr_iid", "body"]
			}`,
		},
		{
			Name:        "gitlab_post_discussion",
			Description: "在 GitLab MR 的具体代码行上创建讨论（行内评论）",
			ParametersJSON: `{
				"type": "object",
				"properties": {
					"project_id": {"type": "integer", "description": "GitLab project ID"},
					"mr_iid": {"type": "integer", "description": "Merge Request IID"},
					"file_path": {"type": "string", "description": "文件路径"},
					"new_line": {"type": "integer", "description": "diff 中的新文件行号"},
					"body": {"type": "string", "description": "评论内容"}
				},
				"required": ["project_id", "mr_iid", "file_path", "new_line", "body"]
			}`,
		},
	}
	return nil
}

// CallTool executes a tool provided by this plugin.
func (p *GitLabPlugin) CallTool(req *proto.CallToolRequest, resp *proto.CallToolResponse) error {
	var args map[string]any
	if err := json.Unmarshal([]byte(req.ArgsJSON), &args); err != nil {
		resp.Error = fmt.Sprintf("parse args: %v", err)
		return nil
	}

	result, err := p.executeTool(req.Name, args)
	if err != nil {
		resp.Error = err.Error()
		return nil
	}

	resultJSON, _ := json.Marshal(result)
	resp.ResultJSON = string(resultJSON)
	return nil
}

func (p *GitLabPlugin) executeTool(name string, args map[string]any) (any, error) {
	projectID := intArg(args, "project_id")
	mrIID := intArg(args, "mr_iid")

	switch name {
	case "gitlab_get_mr_diff":
		return p.glClient.GetMRDiff(projectID, mrIID, p.config.MaxDiffLines, p.config.IgnorePatterns)
	case "gitlab_post_comment":
		body, _ := args["body"].(string)
		return p.glClient.PostComment(projectID, mrIID, body)
	case "gitlab_post_discussion":
		filePath, _ := args["file_path"].(string)
		newLine := intArg(args, "new_line")
		body, _ := args["body"].(string)
		return p.glClient.PostDiscussion(projectID, mrIID, filePath, newLine, body)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// intArg extracts an int from a map[string]any (JSON numbers are float64).
func intArg(args map[string]any, key string) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return 0
}
