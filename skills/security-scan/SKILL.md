---
name: security-scan
description: 分析代码 diff 中的安全和逻辑风险，返回结构化报告
---

# Security Scan Skill

分析代码变更 diff，识别安全漏洞，返回结构化 Markdown 报告。

## 输入

接收 `gitlabGetMrDiff` 返回的 JSON 数组或 unified diff 文本：

```json
[
  {
    "old_path": "src/user.py",
    "new_path": "src/user.py",
    "diff": "@@ -10,3 +10,5 @@\n def login(username):\n-    query = \"SELECT * FROM users WHERE username = ?\"\n+    query = \"SELECT * FROM users WHERE username = '\" + username + \"'\"\n     return db.execute(query)"
  }
]
```

## 输出

结构化 Markdown 报告，包含：
- 概览（问题数量统计）
- 发现的问题列表（文件、行号、问题描述、风险说明、修复建议）
- 结论

## 安全检查规则

### 🔴 Critical（严重）- 必须立即修复

#### 1. 硬编码密钥/凭证
- **检查模式**：
  - `api_key = "xxx"`, `password = "xxx"`, `token = "xxx"`, `secret = "xxx"`
  - `access_key`, `secret_key`, `private_key`
- **风险**：密钥泄露，攻击者可获取系统访问权限
- **修复建议**：使用环境变量或密钥管理服务

#### 2. SQL 注入
- **检查模式**：
  - 字符串拼接构建 SQL：`"SELECT * FROM users WHERE id = " + user_id`
  - f-string 拼接：`f"SELECT * FROM users WHERE name = '{name}'"`
  - 格式化字符串：`"SELECT * FROM users WHERE id = %s" % user_id`
- **风险**：数据泄露、篡改、删除
- **修复建议**：使用参数化查询或 ORM

#### 3. 命令注入
- **检查模式**：
  - 用户输入传递给 `exec`, `system`, `shell`, `popen`, `eval`
  - `os.system(user_input)`, `subprocess.call(cmd, shell=True)`
  - `eval(user_code)`, `exec(user_code)`
- **风险**：执行任意系统命令
- **修复建议**：使用白名单验证，避免 `shell=True`

#### 4. 路径遍历
- **检查模式**：
  - 未验证的文件路径：`open(user_path)`, `readFile(req.query.file)`
  - 路径拼接未验证：`os.path.join(base_dir, user_input)`
- **风险**：读取任意文件（如 `/etc/passwd`）
- **修复建议**：验证路径在允许目录内，使用白名单

#### 5. 反序列化漏洞
- **检查模式**：
  - `pickle.loads(user_data)`, `yaml.load(user_data)` (不安全版本)
  - `json.loads()` 配合 `__reduce__`
- **风险**：执行任意代码
- **修复建议**：使用 `yaml.safe_load()`，避免反序列化不可信数据

#### 6. 敏感凭证泄露（日志/接口）
- **检查模式**：
  - 日志输出密码：`log.info(f"User login: {username}/{password}")`
  - 日志输出 token：`logger.debug(f"Token: {access_token}")`
  - 日志输出 AK/SK：`print(f"AccessKey: {ak}, SecretKey: {sk}")`
  - 接口返回敏感信息：`return {"user": user, "password": password}`
  - 错误消息包含凭证：`throw new Error("Invalid token: " + token)`
- **风险**：凭证通过日志或接口泄露
- **修复建议**：日志脱敏，接口移除敏感字段

### 🟠 High（高危）- 应尽快修复

#### 1. XSS 漏洞（跨站脚本）
- **检查模式**：
  - 未转义的用户输入：`innerHTML = user_input`, `document.write(user_data)`
  - React 中使用 `dangerouslySetInnerHTML`
  - 模板引擎未转义：`{{ user_data | safe }}`
- **风险**：注入恶意脚本，窃取用户信息
- **修复建议**：使用 `textContent`，对用户输入进行 HTML 转义

#### 2. CSRF 缺失（跨站请求伪造）
- **检查模式**：
  - POST/PUT/DELETE 请求无 CSRF token
  - 敏感操作缺少 CSRF 保护
- **风险**：伪造用户请求
- **修复建议**：添加 CSRF token 验证，使用 SameSite Cookie

#### 3. 不安全的加密算法
- **检查模式**：
  - MD5, SHA1 用于密码：`hashlib.md5(password)`, `hashlib.sha1(password)`
  - DES, RC4 等弱加密算法
- **风险**：密码容易被破解
- **修复建议**：使用 bcrypt, argon2, PBKDF2

#### 4. 权限检查缺失
- **检查模式**：
  - 敏感操作未验证权限
  - 直接访问资源，无权限检查
  - 缺少身份验证中间件
- **风险**：未授权访问
- **修复建议**：添加权限验证中间件，实现 RBAC

#### 5. 代码死循环
- **检查模式**：
  - 无退出条件的循环：`while True:` 无 break
  - 条件永远为真：`while 1 == 1:`
  - 递归无终止条件：`def func(): func()`
  - 循环条件永不改变：`while x > 0:` 但 x 从不递减
  - 异步任务无限重试：`while not success: retry()` 无退出机制
- **风险**：CPU 占用 100%，服务不可用
- **修复建议**：添加退出条件、超时机制、循环计数器限制

#### 6. 线程/进程死锁
- **检查模式**：
  - 多个锁的获取顺序不一致
  - 嵌套锁未释放：`lock.acquire()` 后异常导致未 `release()`
  - 循环等待：A 等 B，B 等 C，C 等 A
- **风险**：线程永久阻塞，服务挂起
- **修复建议**：统一锁顺序，使用 `with` 语句，添加超时

### 🟡 Medium（中危）- 建议修复

#### 1. 无索引的数据库查询
- **检查模式**：
  - 全表扫描：`SELECT * FROM large_table WHERE unindexed_column = ?`
  - JOIN 操作未使用索引列
  - LIKE 以通配符开头：`WHERE name LIKE '%keyword%'`
  - 函数导致索引失效：`WHERE YEAR(date_column) = 2024`
- **风险**：查询响应慢，数据库负载高
- **修复建议**：添加索引，使用 EXPLAIN 分析查询计划

#### 2. 资源泄露
- **检查模式**：
  - 文件打开后未关闭：`f = open(file)` 无 `f.close()`
  - 数据库连接未释放：`conn = db.connect()` 无 `conn.close()`
  - 网络连接未关闭：`socket.connect()` 无 `socket.close()`
  - 全局变量无限增长：`global_cache[key] = value` 无清理
  - 缓存无限增长：`cache.set(key, value)` 无过期或淘汰策略
  - 事件监听器未移除：`addEventListener()` 无 `removeEventListener()`
  - 定时器未清理：`setInterval()` 无 `clearInterval()`
- **风险**：文件描述符耗尽，内存耗尽（OOM）
- **修复建议**：使用 `with` 语句，实现缓存淘汰策略（LRU、TTL）

#### 3. 不安全的随机数生成
- **检查模式**：
  - `random.random()` 用于安全场景（生成 token、密钥等）
  - `Math.random()` 用于生成 token
- **风险**：可预测的随机数
- **修复建议**：使用 `secrets` 模块（Python）或 `crypto.randomBytes()`（Node.js）

#### 4. 竞态条件
- **检查模式**：
  - TOCTOU (Time-of-check to time-of-use)
  - 先检查后使用，中间无锁
  - 多线程访问共享变量无同步
- **风险**：数据不一致
- **修复建议**：使用原子操作或锁

### 🟢 Low（低危）- 可选修复

#### 1. 代码质量问题
- 未处理的异常
- 空指针引用
- 类型不匹配

#### 2. 输入验证不足
- 缺少长度限制
- 缺少格式验证
- 缺少范围检查

## 分析要点

1. **关注变更**：重点分析新增（`+`）和修改的代码行
2. **避免误报**：理解代码逻辑和业务场景，不仅凭模式匹配
3. **提供建议**：给出具体的、可操作的修复建议
4. **保护敏感信息**：不要在输出中包含完整的密钥或密码
