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

在 DMR 主配置（如 `~/.dmr/config.yaml`）的 `plugins[].config` 中写入下列字段。插件 JSON 字段名与下表 `配置键` 一致。

### 配置示例

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
      # 省略 review_prompt_source 且不设 review_prompt / review_prompt_file → 使用内置模板
      max_diff_lines: 2000
      cooldown_seconds: 30
      ignore_patterns:
        - "*.lock"
        - "*.min.js"
        - "vendor/**"
        - "go.sum"
      # 审查进行中禁止合并（见下表 block_merge_during_review）
      block_merge_during_review: true
      # 并发与排队（见下表；不配 max_concurrent_reviews 或置 0 表示不限流）
      max_concurrent_reviews: 2
      max_queued_reviews: 128
```

### 配置项参考（完整）

| 配置键 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `listen` | string | `:9090` | Webhook HTTP 监听地址（如 `:9090` 或 `127.0.0.1:9090`）。 |
| `webhook_secret` | string | 空 | 与 GitLab Webhook 的 Secret Token 一致；为空则不校验 `X-Gitlab-Token`。 |
| `gitlab_url` | string | — | GitLab 根 URL（如 `https://git.example.com`），**必填**。 |
| `gitlab_token` | string | — | 具备读 MR、写评论/讨论、（若启用禁止合并）编辑 MR 的 Personal/Project Access Token，**必填**。 |
| `review_language` | string | `zh-CN` | 写入默认审查 prompt 的语言说明；可与模板内文配合使用。 |
| `review_prompt_source` | string | （见下） | 审查模板来源：`builtin`、`config`、`current`、`external`。**未配置**时按兼容规则推断（见「`review_prompt_source` 与兼容行为」）。**显式**填写但拼写未知：记日志并当作 `builtin`。 |
| `review_prompt` | string | 空 | 含义取决于 `review_prompt_source` / 兼容源：见下表与各模式说明。 |
| `review_prompt_file` | string | 空 | `current` / `external`：仓库内模板文件路径（API 拉取）。`config` 模式请用 `review_prompt` 写相对/绝对路径或 `file:`，勿单独依赖本字段。 |
| `review_prompt_ref` | string | 空 | `current` / `external` 下读取 `review_prompt_file` 的分支/标签/commit；**为空**则使用该 GitLab 项目的 **default_branch**。 |
| `review_prompt_project_path` | string | 空 | 仅 `external`：**命名空间路径**，如 `group/sub/repo`，用于 `GET /projects/:id` 解析项目 ID。 |
| `config_base_dir` | string | 空 | **由 DMR 主进程注入**（主配置文件的目录）。`config` 模式下相对路径模板以此为基准，且必须落在该目录树内（防止 `../` 逃逸）。 |
| `max_diff_lines` | int | `2000` | `gitlabGetMrDiff` 侧按单文件 diff 行数过滤的上限；超过则跳过该文件块。 |
| `ignore_patterns` | string[] | 见代码 | 按简单 glob 忽略不参与 diff 输出的路径（如 `vendor/**`）。 |
| `cooldown_seconds` | int | `30` | 同一 `project_id + mr_iid` 在冷却时间内重复 webhook 将返回 `skipped`，不触发审查。 |
| `block_merge_during_review` | bool | `false` | 为 `true` 时，在单次 `RunAgent` 期间尝试禁止合并：优先 `PUT work_in_progress: true`，失败则临时给标题加 `WIP: `，结束后恢复；MR 已是 WIP 则不改。需 Token 有编辑 MR 权限，且实例/项目策略需将 WIP/进行中视为不可合并。 |
| `max_concurrent_reviews` | int | `0` | `0`：不限流，每个请求单独 `go` 起审查。`>0`：固定数量 worker，同时执行的审查不超过该值。 |
| `max_queued_reviews` | int | `64`* | 仅在 `max_concurrent_reviews > 0` 时有效：等待队列缓冲长度。`*` 表示未配置或 `≤0` 时插件内默认 **64**。worker 全忙时任务先入队；**队列满**返回 HTTP **503**，且不消耗该 MR 的 cooldown，便于重试。 |

### 审查期间禁止合并（`block_merge_during_review`）

- 依赖 GitLab 对「进行中 / WIP」MR 的合并限制，插件**不能**在服务端强制加锁，只能通过 API 把 MR 标成不可合并状态。
- 若 `work_in_progress` API 不可用（版本或权限），会回退修改标题；审查结束会撤销；若恢复失败需人工核对标题。

### 并发与限流（`max_concurrent_reviews` / `max_queued_reviews`）

- 限流开启后，任务进入有界队列，由 N 个 worker 顺序消费；适合压低对 LLM、GitLab API 与 DMR 主进程的压力。
- 队列满时响应体为 JSON，例如：`{"status":"rejected","reason":"review queue full (max concurrent + buffer exceeded)"}`；是否由 GitLab 自动重试取决于 Webhook 与网络侧配置。

### `review_prompt_source` 与兼容行为

| 显式 `review_prompt_source` | 模板正文来源 |
|----------------------------|--------------|
| `builtin` | 插件内常量 `DefaultReviewPrompt`。忽略 `review_prompt`、`review_prompt_file`、`review_prompt_project_path`。 |
| `config` | `review_prompt`：可为 **内联** Go `text/template`，或 **主机上的路径**（见下节「config 模式与本地路径」）。 |
| `current` | **必填** `review_prompt_file`。Webhook 对应 **MR 所在项目**（`project_id`）经 API 拉取文件；`review_prompt_ref` 空 → 该项目 `default_branch`。API 失败时可回退：`review_prompt` 非空则作内联模板，否则 `DefaultReviewPrompt`。 |
| `external` | **必填** `review_prompt_file` + `review_prompt_project_path`（如 `org/templates`）。先解析外部项目 ID，再在该项目上拉取文件；`review_prompt_ref` 空 → **外部项目**的 `default_branch`。失败时回退逻辑同 `current`。**Token 须能读取外部项目**的仓库文件（同实例时一般为读库权限；跨组/私有项目需相应授权）。 |

**未写 `review_prompt_source`（空字符串）** — 与旧版配置兼容：

1. 若 `review_prompt_file` 非空 → 行为同 **`current`**（与此前「只配文件路径」一致）。
2. 否则若 `review_prompt` 非空 → 行为同 **`config`**（含本地路径识别）。
3. 否则 → **`builtin`**。

迁移建议：新部署可显式写 `review_prompt_source`，避免依赖上述推断。

### config 模式与本地路径

当来源为 `config`（或兼容推断的 config）时，`review_prompt` 可以是：

- **内联模板**：普通多行字符串，作为 `text/template`。
- **主机文件路径**，若满足任一规则则按文件读取（UTF-8 全文作为模板）：
  - 前缀 `file:`，例如 `file:./gitlab-mr.md`；
  - 以 `./`、`../` 开头；
  - 绝对路径（由 `filepath.IsAbs` 判定，含 Unix `/…` 与 Windows 盘符形式）。

相对路径相对于 DMR 注入的 **`config_base_dir`**（主配置所在目录）；解析后会校验**最终路径必须位于 `config_base_dir` 目录树下**，禁止 `../` 逃逸到配置目录之外。

读文件失败：记日志并回退 **`DefaultReviewPrompt`**（不把错误路径当作内联模板，以免误渲染）。

### builtin / 仓库模板 示例

```yaml
# 显式内置模板
review_prompt_source: builtin

# 内联（config）
review_prompt_source: config
review_prompt: "请审查 MR #{{.MRIID}} …"

# 主配置同目录下的文件（config）
review_prompt_source: config
review_prompt: ./gitlab-mr.md

# 当前 MR 仓库中的文件（current）
review_prompt_source: current
review_prompt_file: .dmr/review.tpl
review_prompt_ref: ""   # 留空则 default_branch

# 另一 GitLab 项目中的模板（external；Token 须能读该项目）
review_prompt_source: external
review_prompt_project_path: "org/templates"
review_prompt_file: review/mr.tpl
review_prompt_ref: main
```

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
        1. 使用 gitlabGetMrDiff 获取变更（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
        2. 重点关注：SQL 注入、XSS、敏感信息泄露、权限绕过
        3. 使用 gitlabPostComment 发布审查总结（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
        4. 使用 gitlabPostDiscussion 对问题代码添加行内评论（project_id={{.ProjectID}}, mr_iid={{.MRIID}}）
        5. 在完成 comment/discussion 之后，调用 `tape.handoff` 为当前 tape 添加一个 anchor，避免下一次审查把本次历史全部带入上下文。
           - name 建议：gitlab:mr:{{.MRIID}}:review-done:<UTC时间戳>
             （UTC 时间戳格式建议：`20060102-150405`）
           - summary 可选：review completed for mr_iid={{.MRIID}} at <UTC时间戳>

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
- `tape.handoff` 来自 DMR 内置 `tape` 插件，请确保在 `~/.dmr/config.yaml` 的 `plugins` 中 `tape` 为 `enabled: true`。
- 如果你自定义了 `review_prompt`，需要自行在末尾加入等价步骤；否则模型可能不会执行 handoff，导致上下文膨胀。
