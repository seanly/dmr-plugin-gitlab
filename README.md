# dmr-plugin-gitlab

DMR 外部插件，接收 GitLab Merge Request webhook 事件，通过 DMR agent loop 执行代码审查，
结果以 comment + inline discussion 形式回写 GitLab。

## 架构

```
GitLab ── Webhook POST ──▶ dmr-plugin-gitlab ──▶ DMR Host (RunAgent)
                                │                       │
                                ├── gitlab_get_mr_diff   ├── Agent Loop
                                ├── gitlab_post_comment  ├── Tape 记录
                                └── gitlab_post_discussion└── OPA Policy
```

插件通过 HashiCorp go-plugin (net/rpc) 与 DMR 主进程通信，利用 MuxBroker 反向 RPC 触发 agent.Run()。

## 构建 & 安装

```bash
make build    # 编译
make install  # 安装到 ~/.dmr/plugins/
```

## 配置

在 `~/.dmr/config.yaml` 中添加：

```yaml
plugins:
  - name: gitlab_reviewer
    enabled: true
    path: ~/.dmr/plugins/dmr-plugin-gitlab
    config:
      listen: ":9090"
      webhook_secret: "your-secret"
      gitlab_url: "https://gitlab.example.com"
      gitlab_token: "glpat-xxxxxxxxxxxx"
      review_language: "zh-CN"
      max_diff_lines: 2000
      cooldown_seconds: 30
      ignore_patterns:
        - "*.lock"
        - "*.min.js"
        - "vendor/**"
        - "go.sum"
```

### 自定义审查 Prompt

`review_prompt` 字段支持 Go `text/template` 语法，可自定义审查指令。不配置则使用默认模板。

可用模板变量：

| 变量 | 说明 |
|------|------|
| `{{.ProjectID}}` | GitLab 项目 ID |
| `{{.ProjectName}}` | 项目名称 |
| `{{.MRIID}}` | MR 编号 |
| `{{.Title}}` | MR 标题 |
| `{{.Description}}` | MR 描述 |
| `{{.SourceBranch}}` | 源分支 |
| `{{.TargetBranch}}` | 目标分支 |
| `{{.ReviewLanguage}}` | 审查语言 |

示例：

```yaml
plugins:
  - name: gitlab_reviewer
    config:
      review_prompt: |
        你是一个资深安全审计员。请审查 MR #{{.MRIID}}（{{.Title}}）。
        项目: {{.ProjectName}} (ID: {{.ProjectID}})
        分支: {{.SourceBranch}} → {{.TargetBranch}}

        步骤：
        1. 使用 gitlab_get_mr_diff 获取变更（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
        2. 重点关注：SQL 注入、XSS、敏感信息泄露、权限绕过
        3. 使用 gitlab_post_comment 发布审查总结（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
        4. 使用 gitlab_post_discussion 对问题代码添加行内评论（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）

        审查语言：{{.ReviewLanguage}}
```

## GitLab Webhook 配置

在 GitLab 项目 Settings → Webhooks 中添加：
- URL: `http://<your-host>:9090/webhook/gitlab`
- Secret Token: 与配置中的 `webhook_secret` 一致
- Trigger: Merge request events

## 提供的 Tools

| Tool | 说明 |
|------|------|
| `gitlab_get_mr_diff` | 获取 MR 的代码变更 diff |
| `gitlab_post_comment` | 在 MR 上发布整体审查评论 |
| `gitlab_post_discussion` | 在 MR 具体代码行上创建讨论 |

## Tape 命名

每个 MR 使用独立 tape：`gitlab:<project_id>:mr:<mr_iid>`
