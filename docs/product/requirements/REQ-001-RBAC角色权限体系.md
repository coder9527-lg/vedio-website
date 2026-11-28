# REQ-001 RBAC 角色权限体系（含产品经理内容管理员角色）

- 类型：功能（架构级）
- 优先级：M（本需求立项后）；在路线图中原为 Later/CAND-01，本次正式立项
- 状态：In Dev
- 提出方 / PM / Dev：Owner / PM 角色（兼任）/ Dev
- 关联路线图：CAND-01 多用户与角色

## 1. 背景与问题陈述

当前系统只有"单访问密码 + HMAC 签名 Cookie"的二值认证：要么能进、要么不能进，没有用户身份与角色区分。随着片库管理能力（刮削、标签、合集、分享、数据清理、转码管理）增多，需要让不同身份的人承担不同职责，尤其需要一个**内容管理员（产品经理角色的系统映射）**来管理片库内容，但不能触碰系统级配置与账号管理。

痛点：
- 家庭/小团队中希望有人负责整理片库（元信息、标签、合集、分享），但不应能改访问密码、清理数据库、终止转码任务。
- 普通观看者不应看到管理入口，更不能调用管理类 API。
- 匿名访客只能通过分享链接看被分享的单个视频。

## 2. 目标与非目标

### 目标
1. 在保留"零数据库默认、单密码可用"基线的前提下，引入可选的多用户 RBAC。
2. 内置 4 个固定角色：管理员（admin）、内容管理员/产品经理（pm）、观看者（viewer）、匿名访客（guest，无需登录态）。
3. 服务端按权限点（permission）做接口级鉴权，前端按角色显隐入口（仅体验层，不作为安全边界）。
4. 引导出第一个管理员账号；支持管理员在 `/admin/users` 管理用户与角色。
5. 未启用多用户时，系统行为与现状完全一致（单密码登录者视同 admin 全权）。

### 非目标（本期不做）
- 不做用户自助注册（用户由管理员创建）。
- 不做自定义角色/动态权限编辑（角色与权限矩阵代码内置，保证可审计）。
- 不做行级/数据级隔离（所有登录用户共享同一片库；仅做功能权限，不做"某用户只能看某目录"）。
- 不做 OAuth/LDAP/第三方登录、不做多因子认证。
- 不做会话表/服务端会话吊销列表（沿用无状态签名 Cookie，改密/改角色后旧 Cookie 在 TTL 内仍有效，靠短 TTL 与版本号缓解，见 §7）。

## 3. 角色与权限矩阵

角色为用户与角色多对一（一个用户一个角色），权限点按角色静态映射。

权限点分组与矩阵（✓=允许，—=拒绝）：

| 权限点 | 说明 / 代表接口 | guest 匿名 | viewer | pm 内容管理员 | admin 管理员 |
| --- | --- | --- | --- | --- | --- |
| `library.read` | 浏览/搜索/详情/播放/下载/海报/字幕 | ✓（仅分享链路） | ✓ | ✓ | ✓ |
| `library.scan` | 触发重扫 `POST /api/v1/rescan` | — | — | — | ✓ |
| `metadata.edit` | 元信息编辑、TMDB 单视频/批量刮削 | — | — | ✓ | ✓ |
| `content.organize` | 标签、合集/播放列表、智能清单、书签、片头跳过 | — | ✓（个人数据） | ✓ | ✓ |
| `personal.rating` | 个人评分、稍后再看、个人进度/收藏/历史 | — | ✓ | ✓ | ✓ |
| `share.manage` | 创建/吊销匿名分享、分享管理中心 | — | — | ✓ | ✓ |
| `transcode.admin` | 转码任务查看/终止 | — | — | — | ✓ |
| `system.health` | 数据健康、缓存健康、存储分析、转码管理页 | — | — | — | ✓ |
| `user.manage` | 用户与角色管理 `/admin/users` | — | — | — | ✓ |

说明：
- **viewer 的 `content.organize` 仅限个人数据**：播放列表、书签、评分、收藏、稍后再看、进度都挂在自己账号下；本期不做数据归属隔离，故 viewer 与 pm/admin 实际共享底层记录（无行级隔离，已列入非目标）。权限点仍保留以便未来加归属。
- **pm（产品经理内容管理员）边界**：可整理内容（刮削、元信息、标签、合集、智能清单、分享审核），但**不能**扫描控制、转码任务管理、数据/缓存清理、用户与角色管理、系统配置。
- guest 不是可登录账号，是未携带有效登录态时的身份，仅能走 `/share/{token}` 公开链路与静态资源。

## 4. 数据模型（SQLite，仅 DB 模式）

新增 `users` 表（幂等迁移，`CREATE TABLE IF NOT EXISTS`）：

```sql
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,              -- bcrypt 哈希
    role          TEXT NOT NULL DEFAULT 'viewer',
    token_version INTEGER NOT NULL DEFAULT 1, -- 改密/改角色递增，使旧 Cookie 失效
    created_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_role ON users(role);
```

- 密码使用 bcrypt（`golang.org/x/crypto/bcrypt`），不存明文。
- 角色取值约束在应用层白名单：`admin | pm | viewer`。
- Prune 不涉及 users（用户不随片库视频清理）。

## 5. 认证令牌（扩展现有无状态 Cookie）

现有令牌 payload 仅为 `exp`。多用户模式扩展为 `exp.uid.role.ver`，仍用 HMAC-SHA256 签名，格式 `payload.sig`：

- 单密码模式（无 users 表/未启用多用户）：保持 `exp.sig` 旧格式，登录者视为内置 admin 全权，**完全向后兼容**。
- 多用户模式：payload 含用户 ID、角色、token_version；校验时比对 DB 中 `token_version`，不一致则拒绝（支撑改密/改角色即时失效）。
- Cookie 名、HttpOnly、SameSite=Lax、Secure、TTL 沿用现有配置。

## 6. 鉴权中间件与降级

### 6.1 双模式判定
- `multiUserEnabled()`：`store != nil` 且 users 表中至少存在一个用户。
- 单密码模式：沿用 `requireAuth`，通过即注入内置 admin 身份。
- 多用户模式：`requireAuth` 解析出用户身份注入 context；`requirePerm(perm)` 包装器按角色矩阵判定，失败时 API 返回 403 JSON、页面 302 到无权限提示页（或首页带 flash）。

### 6.2 上下文与判定
- context key 存 `CurrentUser{ID, Username, DisplayName, Role}`。
- 权限判定函数 `roleHasPerm(role, perm) bool` 由内置矩阵驱动，单点维护。
- 公开路径白名单（`/login`、`/healthz`、`/static/`、`/share/`、占位图、favicon）保持不变。

### 6.3 降级矩阵
| 部署形态 | 认证行为 |
| --- | --- |
| 无 AUTH_PASSWORD、无 DB | 全站公开（现状），所有操作视同 admin（单机自用） |
| 有 AUTH_PASSWORD、无 DB/无用户 | 单密码登录，登录者全权 admin（现状，零改造路径） |
| 有 DB、未建任何用户 | 同上，单密码全权；管理页提示可创建用户启用多用户 |
| 有 DB 且已建用户 | 进入多用户模式，登录需用户名+密码，按角色鉴权；单密码不再作为入口 |
| 仅配 ADMIN_USERNAME/ADMIN_PASSWORD（无 AUTH_PASSWORD）、有 DB | 空表时引导首个 admin 并自动进入多用户模式；认证器用占位口令构造，单密码入口不开放，只能用户名+密码登录 |

> 说明：多用户模式同样依赖认证器签发/校验 HMAC 身份令牌。未显式配置 `AUTH_PASSWORD` 但触发了管理员引导时，装配层用内部占位口令构造认证器；该口令不接受任何登录请求，多用户登录一律走 users 表的 bcrypt 校验。

## 7. 管理员引导与用户管理

- 引导（bootstrap）：新增环境变量 `ADMIN_USERNAME` + `ADMIN_PASSWORD`。服务启动且多用户表为空时，若两者齐全则创建首个 admin；已存在用户则忽略（不覆盖）。未配置则不自动建用户，维持单密码模式。引导依赖 DB_PATH；无 DB 时即便配置了变量也不生效（RBAC 整体不启用）。仅配置引导变量而未配置 AUTH_PASSWORD 时，会自动启用认证并直接进入多用户模式，单密码入口不开放。
- `/admin/users`（需 `user.manage`）：列出用户、创建用户（用户名/显示名/密码/角色）、重置密码、改角色、禁用/删除（保护最后一个 admin 不可删/降级）。
- 修改密码或角色时 `token_version++`，使该用户旧 Cookie 提前失效。
- 登录失败限流复用现有按 IP FailLimiter。

## 8. 验收标准

### 正常路径
1. 给定已配置 `ADMIN_USERNAME/ADMIN_PASSWORD` 且 DB 可用，当服务首次启动，那么 users 表出现一个 admin 账号且能用其登录 `/admin/users`。
2. 给定 admin 创建一个 pm 用户，当该 pm 登录，那么能访问元信息编辑/刮削/标签/合集/分享管理，但访问 `/admin/users`、转码管理、数据健康时被拒绝（API 403/页面拦截）。
3. 给定 viewer 登录，当调用 `POST /api/v1/rescan` 或元信息编辑接口，那么返回 403，且导航不显示这些入口。
4. 给定任意登录用户，当访问浏览/播放/个人评分等其角色允许的能力，那么行为与现状一致。

### 边界路径
5. 当 users 表为空时，系统保持单密码模式，既有登录与全部功能不回归。
6. 当尝试删除/降级最后一个 admin，那么操作被拒绝并提示。
7. 角色字段传入非白名单值时返回 400，不落库。

### 异常路径
8. 未登录访问受保护 API 返回 401；登录但权限不足返回 403，二者语义区分。
9. 用户名重复创建返回 409；弱约束（用户名非空、密码最小长度）不满足返回 400。
10. 令牌签名错误/过期/token_version 不匹配一律按未认证处理（401/跳登录）。

### 降级路径
11. 未配置 DB 时，不渲染用户管理入口，相关 API 返回 503，单密码与公开模式正常。
12. 匿名访客仅能访问 `/share/*` 与公开静态资源，访问其他路径按现有认证规则拦截。

## 9. 依赖与降级
- 依赖能力：DB_PATH（多用户必须）、AUTH_PASSWORD（可选，单密码模式）、新增 `ADMIN_USERNAME/ADMIN_PASSWORD`（仅引导）。
- 新增 Go 依赖：`golang.org/x/crypto/bcrypt`（Go 官方扩展，纯 Go 可静态编译，不破坏无 CGO）。
- 降级：无 DB 时整套 RBAC 不启用，回到现状；不影响零数据库默认。

## 10. 影响面与兼容性
- 新增：`internal/store/user_store.go`、auth 角色/权限/令牌扩展、`rbac_handlers.go`、`admin_users.html`、登录页加用户名字段（多用户模式）。
- 改造：`server.go` 中间件链与路由分组、`auth_handlers.go` 登录逻辑、`basePage` 注入 CurrentUser、各管理模板按角色显隐、config 与 main 引导。
- 兼容：单密码 Cookie 旧格式继续可解析；无 DB/无用户时零行为变化；URL 与既有 API 路径不变，仅新增 403 语义与 `/admin/users`。
- schema：仅新增 users 表与索引，不改既有表；回滚=停用新表（旧代码忽略该表）。
- SW 缓存：登录页/管理页等静态模板变更需升级 `CACHE_NAME` 并同步 PWA 测试断言。

## 11. 分期落地
1. store：users 表 + 用户仓储 + bcrypt + 单测。
2. auth：角色常量、权限矩阵、双格式令牌。
3. server：context + 中间件 + 双模式兼容。
4. 登录/登出改造 + 用户管理页/API。
5. 路由分组接入 + 模板显隐 + 引导装配。
6. HTTP 测试覆盖 401/403/降级/引导，全量 build/vet/test 门禁。

## 12. 度量
- 管理动作可审计：用户创建/角色变更在日志留痕（用户名、操作、IP）。
- 权限拦截可观测：403 计数（日志），用于发现误配权限。
- 零回归：单密码/公开模式既有 HTTP 测试全绿。
