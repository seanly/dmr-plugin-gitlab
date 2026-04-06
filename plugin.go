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
func (p *GitLabPlugin) SetHostClient(client any) {
	c, ok := client.(*rpc.Client)
	if !ok {
		log.Printf("dmr-plugin-gitlab: SetHostClient received unexpected type %T", client)
		return
	}
	log.Printf("dmr-plugin-gitlab: SetHostClient called (server=%v)", p.server != nil)
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
// When called in a GitLab-triggered RunAgent context, project_id and mr_iid are
// automatically provided from the webhook context; these tools do not accept
// explicit project_id/mr_iid parameters and always operate on the current MR.
func (p *GitLabPlugin) ProvideTools(req *proto.ProvideToolsRequest, resp *proto.ProvideToolsResponse) error {
	resp.Tools = []proto.ToolDef{
		{
			Name:           "gitlabGetMrDiff",
			Description:    "获取当前 GitLab MR 的代码变更 diff",
			ParametersJSON: `{"type": "object", "properties": {}}`,
			Group:          "extended",
			SearchHint:     "gitlab, mr, diff, changes, code review, 代码变更, 差异",
		},
		{
			Name:           "gitlabGetMrMeta",
			Description:    "获取当前 GitLab MR 元数据（标题、分支、web_url、作者 username/id；author_email 在 Token 权限允许时返回）",
			ParametersJSON: `{"type": "object", "properties": {}}`,
			Group:          "extended",
			SearchHint:     "gitlab, mr, metadata, info, author, 元数据, 信息",
		},
		{
			Name:           "gitlabPostComment",
			Description:    "在当前 GitLab MR 上发布评论（整体审查总结）",
			ParametersJSON: `{"type": "object", "properties": {"body": {"type": "string", "description": "Markdown 格式的评论内容"}}, "required": ["body"]}`,
			Group:          "extended",
			SearchHint:     "gitlab, mr, comment, review, 评论, 评审",
		},
		{
			Name:           "gitlabPostDiscussion",
			Description:    "在当前 GitLab MR 的具体代码行上创建讨论（行内评论）",
			ParametersJSON: `{"type": "object", "properties": {"file_path": {"type": "string", "description": "文件路径"}, "new_line": {"type": "integer", "description": "MR 新文件中的行号（与 gitlabGetMrDiff 里该文件一致）。插件会从 MR unified diff 自动补全 old_line/old_path，以便 GitLab 13.x 在「变更」里显示锚点"}, "body": {"type": "string", "description": "评论内容"}}, "required": ["file_path", "new_line", "body"]}`,
			Group:          "extended",
			SearchHint:     "gitlab, mr, discussion, inline, comment, 讨论, 行内评论",
		},
		{
			Name:           "gitlabAddWebhook",
			Description:    "为 GitLab 项目添加 Merge Request webhook",
			ParametersJSON: `{"type": "object", "properties": {"project_path": {"type": "string", "description": "项目路径（path_with_namespace），如 group/my-project"}, "url": {"type": "string", "description": "Webhook 接收 URL"}, "token": {"type": "string", "description": "Secret token（可选，用于验证 webhook 请求来源）"}}, "required": ["project_path", "url"]}`,
			Group:          "extended",
			SearchHint:     "gitlab, webhook, setup, configure, webhook, 配置",
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

	// Parse context from the request (passed from RunAgent)
	toolCtx := make(map[string]any)
	if req.ContextJSON != "" {
		if err := json.Unmarshal([]byte(req.ContextJSON), &toolCtx); err != nil {
			log.Printf("dmr-plugin-gitlab: CallTool %s failed to parse context JSON: %v", req.Name, err)
		}
	}

	result, err := p.executeTool(req.Name, args, toolCtx)
	if err != nil {
		resp.Error = err.Error()
		return nil
	}

	resultJSON, _ := json.Marshal(result)
	resp.ResultJSON = string(resultJSON)
	return nil
}

func (p *GitLabPlugin) executeTool(name string, args, toolCtx map[string]any) (any, error) {
	switch name {
	case "gitlabGetMrDiff":
		projectID := intArgWithContext(args, toolCtx, "project_id")
		mrIID := intArgWithContext(args, toolCtx, "mr_iid")
		if projectID == 0 || mrIID == 0 {
			return nil, fmt.Errorf("project_id and mr_iid are required (or must be provided in context)")
		}
		return p.glClient.GetMRDiff(projectID, mrIID, p.config.MaxDiffLines, p.config.IgnorePatterns)
	case "gitlabGetMrMeta":
		projectID := intArgWithContext(args, toolCtx, "project_id")
		mrIID := intArgWithContext(args, toolCtx, "mr_iid")
		if projectID == 0 || mrIID == 0 {
			return nil, fmt.Errorf("project_id and mr_iid are required (or must be provided in context)")
		}
		return p.glClient.GetMRMeta(projectID, mrIID)
	case "gitlabPostComment":
		projectID := intArgWithContext(args, toolCtx, "project_id")
		mrIID := intArgWithContext(args, toolCtx, "mr_iid")
		if projectID == 0 || mrIID == 0 {
			return nil, fmt.Errorf("project_id and mr_iid are required (or must be provided in context)")
		}
		body, _ := args["body"].(string)
		return p.glClient.PostComment(projectID, mrIID, body)
	case "gitlabPostDiscussion":
		projectID := intArgWithContext(args, toolCtx, "project_id")
		mrIID := intArgWithContext(args, toolCtx, "mr_iid")
		if projectID == 0 || mrIID == 0 {
			return nil, fmt.Errorf("project_id and mr_iid are required (or must be provided in context)")
		}
		filePath, _ := args["file_path"].(string)
		newLine := intArg(args, "new_line")
		body, _ := args["body"].(string)
		return p.glClient.PostDiscussion(projectID, mrIID, filePath, newLine, body)
	case "gitlabAddWebhook":
		projectPath, _ := args["project_path"].(string)
		webhookURL, _ := args["url"].(string)
		token, _ := args["token"].(string)
		return p.glClient.AddProjectWebhook(projectPath, webhookURL, token)
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

// intArgWithContext extracts an int from args, falling back to toolCtx if not present.
// This allows tools to use context values passed from RunAgent as defaults.
func intArgWithContext(args, toolCtx map[string]any, key string) int {
	// First try args
	if v := intArg(args, key); v != 0 {
		return v
	}
	// Fall back to context
	if v, ok := toolCtx[key].(float64); ok {
		return int(v)
	}
	return 0
}
