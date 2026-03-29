# dmr-plugin-gitlab

DMR 外部插件，接收 GitLab Merge Request webhook 事件，通过 DMR agent loop 执行代码审查，
结果以 comment + inline discussion 形式回写 GitLab。

## 架构

```
GitLab ── Webhook POST ──▶ dmr-plugin-gitlab ──▶ DMR Host (RunAgent)
                                │                       │
                                ├── gitlabGetMrDiff   ├── Agent Loop
                                ├── gitlabPostComment  ├── Tape 记录
                                └── gitlabPostDiscussion└── OPA Policy
```

插件通过 HashiCorp go-plugin (net/rpc) 与 DMR 主进程通信，利用 MuxBroker 反向 RPC 触发 agent.Run()。

## 构建 & 安装

```bash
make build    # 编译
make install  # 安装到 ~/.dmr/plugins/
```

## 配置

在 DMR 主配置（默认 `~/.dmr/config.toml`，可参考主仓 `config.example.toml`）中，为每个外部插件使用一段 `[[plugins]]`，并在**紧接其后**放置 `[plugins.config]`（TOML 会把该表归并到上一个插件条目）。下列 **配置键** 与插件 `Init` 收到的 JSON 字段名一致。

### 配置示例

```toml
[[plugins]]
name = "gitlab_reviewer"
enabled = true
path = "~/.dmr/plugins/dmr-plugin-gitlab"

[plugins.config]
listen = ":9090"
webhook_secret = "your-secret"
gitlab_url = "https://gitlab.example.com"
gitlab_token = "glpat-xxxxxxxxxxxx"
review_language = "zh-CN"
# 可选：按 GitLab path_with_namespace 选择审查模板（见下节）
# mr_prompts_file = "mr-review-prompts.json"
max_diff_lines = 2000
cooldown_seconds = 30
ignore_patterns = ["*.lock", "*.min.js", "vendor/**", "go.sum"]
block_merge_during_review = true
max_concurrent_reviews = 2
max_queued_reviews = 128
```

### 配置项参考（完整）

| 配置键 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `listen` | string | `:9090` | Webhook HTTP 监听地址（如 `:9090` 或 `127.0.0.1:9090`）。 |
| `webhook_secret` | string | 空 | 与 GitLab Webhook 的 Secret Token 一致；为空则不校验 `X-Gitlab-Token`。 |
| `gitlab_url` | string | — | GitLab 根 URL（如 `https://git.example.com`），**必填**。 |
| `gitlab_token` | string | — | 具备读 MR、写评论/讨论、（若启用禁止合并）编辑 MR 的 Personal/Project Access Token，**必填**。 |
| `review_language` | string | `zh-CN` | 写入默认审查 prompt 的语言说明；可与模板内文配合使用。 |
| `config_base_dir` | string | 空 | **由 DMR 主进程注入**（主配置文件的目录）。`mr_prompts_file` 及 JSON 内 `file:` 相对路径以此为基准，且必须落在该目录树内（防止 `../` 逃逸）。 |
| `mr_prompts_file` | string | 空 | 可选。指向 `mr-review-prompts.json`（相对路径相对 `config_base_dir`）。不配则始终使用内置 `DefaultReviewPrompt`。 |
| `max_diff_lines` | int | `2000` | `gitlabGetMrDiff` 侧按单文件 diff 行数过滤的上限；超过则跳过该文件块。 |
| `ignore_patterns` | string[] | 见代码 | 按简单 glob 忽略不参与 diff 输出的路径（如 `vendor/**`）。 |
| `cooldown_seconds` | int | `30` | 同一 `project_id + mr_iid` 在冷却时间内重复 webhook 将返回 `skipped`，不触发审查。 |
| `block_merge_during_review` | bool | `false` | 为 `true` 时，在单次 `RunAgent` 期间尝试禁止合并：优先 `PUT work_in_progress: true`，失败则临时给标题加 `WIP: `，结束后恢复；MR 已是 WIP 则不改。需 Token 有编辑 MR 权限，且实例/项目策略需将 WIP/进行中视为不可合并。 |
| `max_concurrent_reviews` | int | `0` | `0`：不限流，每个请求单独 `go` 起审查。`>0`：固定数量 worker，同时执行的审查不超过该值。 |
| `max_queued_reviews` | int | `64`* | 仅在 `max_concurrent_reviews > 0` 时有效：等待队列缓冲长度。`*` 表示未配置或 `≤0` 时插件内默认 **64**。worker 全忙时任务先入队；**队列满**返回 HTTP **503**，且不消耗该 MR 的 cooldown，便于重试。 |

### MR 提示词映射（JSON）

与主配置同目录（或 `config_base_dir` 下）放置映射文件（例如 `mr-review-prompts.json`），并在插件配置中设置 `mr_prompts_file`。

```json
{
  "default": "builtin",
  "by_path": {
    "group/sub/frontend": "file:./prompts/frontend.tmpl",
    "group/sub/backend": "file:./prompts/go-service.tmpl"
  }
}
```

- **查找顺序**：Webhook 的 `project.path_with_namespace` → `by_path` 精确匹配 → 否则 `default`。
- **未配置** `mr_prompts_file`，或 JSON **读失败**，或未匹配 `by_path` 且 JSON **没有**可用的 `default`：使用内置 `DefaultReviewPrompt`。
- **取值**（与原先 `review_prompt` config 模式一致）：`builtin`；`file:...` 或 `config_base_dir` 下相对/绝对路径（读文件）；或其它非空字符串（内联 Go `text/template`）。读文件失败会记日志并回退内置模板。
- **审查结束后发飞书**：见 [`examples/mr-review-with-feishu.tmpl`](examples/mr-review-with-feishu.tmpl)（GitLab 评论 + 飞书）。**只在飞书写结论、不回写 GitLab**：见 [`examples/mr-review-feishu-only.tmpl`](examples/mr-review-feishu-only.tmpl)（依赖提示词禁止 `gitlabPostComment` / `gitlabPostDiscussion`；仍需 `feishu_notify` 与飞书卡片变量对齐）。

### 审查期间禁止合并（`block_merge_during_review`）

- 依赖 GitLab 对「进行中 / WIP」MR 的合并限制，插件**不能**在服务端强制加锁，只能通过 API 把 MR 标成不可合并状态。
- 若 `work_in_progress` API 不可用（版本或权限），会回退修改标题；审查结束会撤销；若恢复失败需人工核对标题。

### 并发与限流（`max_concurrent_reviews` / `max_queued_reviews`）

- 限流开启后，任务进入有界队列，由 N 个 worker 顺序消费；适合压低对 LLM、GitLab API 与 DMR 主进程的压力。
- 队列满时响应体为 JSON，例如：`{"status":"rejected","reason":"review queue full (max concurrent + buffer exceeded)"}`；是否由 GitLab 自动重试取决于 Webhook 与网络侧配置。

### 审查模板变量

内置模板或自定义模板中可使用：

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

自定义模板时请在末尾保留与内置 `DefaultReviewPrompt` 等价的 **tape.handoff** 步骤，否则多次审查可能导致上下文膨胀。

## GitLab Webhook 配置

在 GitLab 项目 Settings → Webhooks 中添加：
- URL: `http://<your-host>:9090/webhook/gitlab`
- Secret Token: 与配置中的 `webhook_secret` 一致
- Trigger: Merge request events

## 提供的 Tools

| Tool | 说明 |
|------|------|
| `gitlabGetMrDiff` | 获取 MR 的代码变更 diff |
| `gitlabGetMrMeta` | 获取 MR 元数据（标题、分支、web_url、作者；`author_email` 需 Token 有足够权限） |
| `gitlabPostComment` | 在 MR 上发布整体审查评论 |
| `gitlabPostDiscussion` | 在 MR 具体代码行上创建讨论 |

## Tape 命名

每个 MR 使用独立 tape：`gitlab:<project_id>:mr:<mr_iid>`

## 上下文截断（handoff）

由于同一 MR 多次触发审查会持续向同一条 tape 追加消息，如果不做阶段切分，后续审查的上下文会越来越长。

因此在每次审查的最后一步，模型需要调用 `tape.handoff`（默认 `DefaultReviewPrompt` 已包含相应指令）。该工具会往当前 tape 追加一个 anchor，后续轮次读取 `LastAnchorContext` 时只会从最后的 anchor 之后继续加载，从而抑制 `messages` 随审查次数线性增长。

注意：
- `tape.handoff` 来自 DMR 内置 `tape` 插件，请在 `~/.dmr/config.toml` 中为 `tape` 保留 `enabled = true`（例如与其它 `[[plugins]]` 并列）。
- 自定义审查模板时，需要自行在末尾加入等价步骤。
