# ManjuFlow Studio

一名导演独立完成 AI 漫剧生产与带教的后端服务。系统把**提示词资产**、**分镜生产编排**与
**教学工坊**串成两条互相依赖的业务链：

- **制作链**：建剧集 → 编排分镜 → 绑定提示词版本 → 提交渲染（幂等键 + 日配额）→ worker 渲染（租约 / 退避重试 / 永久失败）→ 审片 → 发布。
- **带教链**：已发布剧集的已审片分镜开教学工坊 → 学员报名（座位容量）→ 按工坊冻结的提示词版本提交练习 → 导演评审 → 结业。

两条链相互约束：工坊只能建立在 `published` 剧集的 `approved` 分镜上；提示词版本被未完成渲染任务或
未结业工坊引用时不可下架；工坊未结业时对应分镜不能退回改稿。

## 技术栈

| 项 | 选择 |
| --- | --- |
| 语言 | Go 1.22（`GOTOOLCHAIN=local`） |
| 数据库 | SQLite，驱动 `modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0`） |
| HTTP | 标准库 `net/http` + Go 1.22 路由方法模式 |
| 迁移 | `migrations/*.sql` 通过 `embed.FS` 顺序执行，`schema_migrations` 台账 |
| 时区 | 业务时区 `Asia/Shanghai`（镜像内置 `time/tzdata`） |

## 目录结构

```text
cmd/server                  进程入口、信号处理、优雅关闭
internal/app                配置 → 存储 → 服务 → HTTP → worker 的装配与 bootstrap
internal/config             环境变量加载与校验
internal/apperr             稳定错误码、错误链、HTTP 状态映射
internal/clock              业务时区时钟与可控测试时钟
internal/logging            结构化 JSON 日志
internal/reqctx             请求 ID 与调用者身份的 context 传播
internal/security           PBKDF2 口令、不透明会话 token、请求指纹
internal/domain/identity    角色、capability、会话可用性
internal/domain/prompt      提示词模板与不可变版本、下架规则
internal/domain/production  剧集与分镜状态机、发布前置条件
internal/domain/render      渲染任务状态机、租约 fencing、退避
internal/domain/teaching    工坊、报名、练习评审
internal/domain/audit       审计事件值对象
internal/repository         持久化接口、分页与过滤类型
internal/repository/sqliterepo  全部 SQL 实现
internal/storage/sqlitedb   连接、pragma、事务 runner、迁移执行器
internal/idempotency        幂等键生命周期
internal/auditlog           事务内审计写入
internal/service/*          authsvc / promptsvc / productionsvc / rendersvc / teachingsvc
internal/httpapi            路由、处理器、响应视图、统一错误信封
internal/middleware         请求 ID、访问日志、panic recovery、超时、鉴权
internal/worker             渲染 worker 与后台巡检
internal/apptest            端到端测试的确定性支撑代码
migrations                  版本化 SQL 与嵌入加载器
```

## 数据模型

16 张表：`schema_migrations`、`studios`、`users`、`sessions`、`prompt_templates`、`prompt_versions`、
`series`、`shots`、`render_quotas`、`render_jobs`、`workshops`、`enrollments`、`practice_submissions`、
`audit_events`、`idempotency_records`、`business_sequences`。

关键约束：

- 唯一：`users(studio_id,email)`、`sessions(token_hash)`、`prompt_templates(studio_id,slug)`、
  `prompt_versions(template_id,version)`、`series(studio_id,code)`、`shots(series_id,ordinal)`、
  `render_quotas(studio_id,quota_day)`、`enrollments(workshop_id,apprentice_id)`、
  `idempotency_records(studio_id,method,path,key)`；
- 部分唯一索引 `idx_render_jobs_active_shot`：同一分镜最多一个在途渲染任务；
- 并发控制：配额与座位使用条件 `UPDATE`，`series`/`shots`/`workshops`/`practice_submissions` 使用版本号乐观锁，
  渲染任务使用 `lease_generation` fencing，业务编号使用 `INSERT ... ON CONFLICT ... RETURNING` 单语句自增。

## 状态机

- `series`：`draft → shooting → reviewing → published → archived`（`cancelled` 终止分支）
- `shots`：`draft → bound → rendering → rendered → approved`，`rendered|approved → rework → bound`
- `render_jobs`：`queued → leased → succeeded | retrying → queued | failed_permanent`（`cancelled` 终止分支）
- `workshops`：`open → teaching → grading → closed`（`open → grading` 用于无人提交时直接结课，`cancelled` 终止分支）
- `enrollments`：`enrolled → submitted → graded`
- `practice_submissions`：`pending → accepted | returned`（及格线 60）

## HTTP 接口

| 方法与路径 | 说明 |
| --- | --- |
| `GET /healthz` | 存活检查 |
| `GET /readyz` | 就绪检查，校验数据库连通与 schema 版本 |
| `POST /v1/auth/login` | 登录，返回一次性 Bearer token |
| `POST /v1/auth/logout` | 退出并撤销当前会话 |
| `GET /v1/auth/session` | 当前会话与 capability |
| `POST /v1/members` | 导演新增成员 |
| `POST /v1/prompt-templates` | 新建提示词模板 |
| `POST /v1/prompt-templates/{templateID}/versions` | 追加不可变版本 |
| `GET /v1/prompt-templates/{templateID}/versions` | 分页查看版本链 |
| `POST /v1/prompt-versions/{versionID}/activate` | 冻结草稿版本 |
| `POST /v1/prompt-versions/{versionID}/retire` | 下架版本（受引用检查约束） |
| `GET /v1/prompt-versions/{versionID}/references` | 查看在用引用统计 |
| `POST /v1/series` | 新建剧集并取号 |
| `GET /v1/series` | 过滤 / 排序 / 分页列表 |
| `GET /v1/series/{seriesID}` | 剧集详情与分镜进度 |
| `POST /v1/series/{seriesID}/shots` | 批量编排分镜（逐项结果） |
| `POST /v1/series/{seriesID}/publish` | 发布剧集 |
| `POST /v1/shots/{shotID}/prompt` | 绑定提示词版本 |
| `POST /v1/shots/{shotID}/render` | 提交渲染，支持 `Idempotency-Key` |
| `GET /v1/shots/{shotID}/render-jobs` | 渲染历史 |
| `POST /v1/shots/{shotID}/review` | 审片通过或退回改稿 |
| `GET /v1/render-quota` | 当日渲染配额 |
| `POST /v1/workshops` | 开设教学工坊 |
| `GET /v1/workshops` | 工坊列表 |
| `POST /v1/workshops/{workshopID}/enrollments` | 学员报名 |
| `POST /v1/workshops/{workshopID}/submissions` | 学员提交练习 |
| `GET /v1/workshops/{workshopID}/submissions` | 练习列表 |
| `POST /v1/workshops/{workshopID}/grading` | 停止报名进入评分 |
| `POST /v1/workshops/{workshopID}/close` | 结业（需全部评审完成） |
| `POST /v1/practice-submissions/{submissionID}/review` | 评审练习 |

错误统一返回：

```json
{"error": {"code": "failed_precondition", "message": "...", "request_id": "req_...", "details": {"field": "..."}}}
```

## 身份与权限

- 登录返回一次性不透明 Bearer token，服务端只保存其 SHA-256 摘要；
- 退出立即撤销会话并推进 `generation`；过期会话被拒绝并由后台巡检清理；
- 口令使用 PBKDF2-HMAC-SHA256（24000 轮、随机盐、常数时间比较）；
- 两个业务角色：`director`（提示词、剧集、渲染、审片、发布、开课、评审）与
  `apprentice`（报名、提交练习、查看目录）；权限在 Service 层按 capability 统一校验。

## 运行

```bash
cp .env.example .env
make run          # 或 MANJU_DB_PATH=./data/manjuflow.sqlite go run ./cmd/server
curl localhost:8080/readyz
```

未设置 `MANJU_DIRECTOR_PASSWORD` 时，首次启动会生成一次性口令并在日志中打印一次；仓库与镜像不含任何凭据。

## 验证

```bash
go build ./...
go vet ./...
gofmt -l .
go test ./... -count=1
go test -race ./... -count=1
```

容器（双架构）：

```bash
docker build --platform linux/amd64 -t manjuflow-studio:amd64 .
docker build --platform linux/arm64 -t manjuflow-studio:arm64 .
docker image inspect manjuflow-studio:amd64 --format '{{.Os}}/{{.Architecture}}'
docker run -d -p 8080:8080 manjuflow-studio:amd64
curl localhost:8080/healthz && curl localhost:8080/readyz
```
