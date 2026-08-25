# ManjuFlow Studio

一个人导演的 AI 漫剧生产与带教编排后端。导演在同一个系统里维护提示词资产、编排分镜、调度渲染、审片发布，
并把已发布的分镜开成教学工坊带学员练习。

## 业务模型

两条相互依赖的公开业务路径：

**制作链**：建剧集 → 编排分镜 → 绑定提示词版本 → 提交渲染（日配额 + 幂等键）→ worker 渲染（租约、退避重试、永久失败）
→ 分镜审片 → 剧集发布。

**带教链**：已发布剧集的已审片分镜开教学工坊 → 学员报名（座位容量）→ 学员按工坊冻结的提示词版本提交练习
→ 导演评审 → 停止报名进入评分 → 全部评审完成后结课。

两条链相互约束：

- 工坊只能建立在 `published` 剧集的 `approved` 分镜上，并冻结该分镜当时绑定的提示词版本。
- 提示词版本被绑定中的分镜、未完成的渲染任务或未结课的工坊引用时不能下架。
- 分镜在有存活工坊时不能退回改稿，避免学员中途失去参考材料。
- 剧集只有全部分镜审片通过且仍绑定提示词版本时才能发布。

## 角色

| 角色 | 能力 |
| --- | --- |
| `director` | 提示词资产、剧集与分镜编排、绑定版本、提交渲染、审片、发布、开工坊、评审练习 |
| `apprentice` | 浏览目录、报名工坊、提交练习 |

身份是服务端会话：登录返回一次性不透明 Bearer token，数据库只保存其 SHA-256 摘要；退出即撤销，过期即失效，
撤销与重签都会推进会话 `generation`。

## 目录结构

```text
cmd/server              进程入口、信号处理、优雅关闭
internal/app            配置到 HTTP 与 worker 的装配、首启播种
internal/httpapi        路由、处理器、视图投影、统一错误信封
internal/middleware     请求 ID、访问日志、panic 恢复、超时、鉴权
internal/service/*      authsvc / promptsvc / productionsvc / rendersvc / teachingsvc
internal/domain/*       identity / prompt / production / render / teaching / audit
internal/repository     仓储契约、分页与过滤类型
internal/repository/sqliterepo  SQLite 实现（全部 SQL 只在这里）
internal/storage/sqlitedb       连接、pragma、事务边界、迁移执行
internal/idempotency    幂等键生命周期
internal/auditlog       事务内审计记录器
internal/worker         渲染 worker 与周期巡检
internal/apptest        端到端测试用的确定性夹具
migrations              版本化 SQL 迁移（embed.FS）
```

依赖方向：领域层不依赖 HTTP 与数据库；HTTP 不拼 SQL；worker 通过 service 访问业务状态。

## 数据与并发

- SQLite，驱动 `modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0`）。
- 写事务使用 `BEGIN IMMEDIATE`，开启 `foreign_keys` 与 WAL。
- 16 张关联表：`schema_migrations`、`studios`、`users`、`sessions`、`prompt_templates`、`prompt_versions`、
  `series`、`shots`、`render_quotas`、`render_jobs`、`workshops`、`enrollments`、`practice_submissions`、
  `audit_events`、`idempotency_records`、`business_sequences`。
- 并发控制：日配额与工坊座位的条件 `UPDATE`、`series/shots/workshops/practice_submissions` 版本号乐观锁、
  渲染任务的活动唯一索引与 `lease_generation` fencing、业务序列 `ON CONFLICT ... RETURNING` 单语句自增、
  幂等记录四元组唯一约束。
- 迁移：顺序执行并与台账同事务写入；重复启动幂等；已应用脚本被改动或出现未知版本时拒绝启动。

## 运行

```bash
cp .env.example .env
go run ./cmd/server
```

首启会创建工坊与两个角色账号。未设置 `MANJU_DIRECTOR_PASSWORD` / `MANJU_APPRENTICE_PASSWORD` 时，
进程会随机生成一次性口令并只记录一次日志；仓库与镜像中不含任何凭据。

## HTTP 接口

| 方法与路径 | 说明 |
| --- | --- |
| `GET /healthz` | 存活检查 |
| `GET /readyz` | 就绪检查，返回已应用的 schema 版本 |
| `POST /v1/auth/login` | 登录，返回一次性 token |
| `POST /v1/auth/logout` | 退出并撤销当前会话 |
| `GET /v1/auth/session` | 当前会话与能力清单 |
| `POST /v1/members` | 导演添加成员 |
| `POST /v1/prompt-templates` | 新建提示词模板 |
| `POST /v1/prompt-templates/{templateID}/versions` | 追加不可变版本 |
| `GET /v1/prompt-templates/{templateID}/versions` | 分页查看版本链 |
| `POST /v1/prompt-versions/{versionID}/activate` | 冻结草稿版本 |
| `POST /v1/prompt-versions/{versionID}/retire` | 下架版本（受引用约束） |
| `GET /v1/prompt-versions/{versionID}/references` | 查看存活引用来源 |
| `POST /v1/series` | 建剧集并取业务编号 |
| `GET /v1/series` | 按状态、标题过滤分页 |
| `GET /v1/series/{seriesID}` | 剧集详情与分镜进度 |
| `POST /v1/series/{seriesID}/shots` | 批量编排分镜（逐项结果） |
| `POST /v1/series/{seriesID}/publish` | 发布剧集 |
| `POST /v1/shots/{shotID}/prompt` | 绑定提示词版本 |
| `POST /v1/shots/{shotID}/render` | 提交渲染，支持 `Idempotency-Key` |
| `GET /v1/shots/{shotID}/render-jobs` | 渲染历史 |
| `POST /v1/shots/{shotID}/review` | 审片通过或退回改稿 |
| `GET /v1/render-quota` | 当日渲染配额 |
| `POST /v1/workshops` | 开教学工坊 |
| `GET /v1/workshops` | 工坊列表 |
| `POST /v1/workshops/{workshopID}/enrollments` | 学员报名 |
| `POST /v1/workshops/{workshopID}/submissions` | 提交练习 |
| `GET /v1/workshops/{workshopID}/submissions` | 练习分页列表 |
| `POST /v1/workshops/{workshopID}/grading` | 停止报名进入评分 |
| `POST /v1/workshops/{workshopID}/close` | 结课（需全部评审完成） |
| `POST /v1/practice-submissions/{submissionID}/review` | 评审练习 |

错误统一为 `{"error":{"code","message","request_id","details"}}`，`code` 为稳定值：`invalid_argument`、
`unauthenticated`、`permission_denied`、`not_found`、`conflict`、`failed_precondition`、`resource_exhausted`、
`canceled`、`deadline_exceeded`、`internal`。请求 ID 由 `X-Request-Id` 透传或自动生成，并在响应头回显。

## 校验

```bash
go build ./...
go vet ./...
gofmt -l .
go test ./... -count=1
go test -race ./... -count=1
```

容器（两个目标架构分别构建、核对架构、启动并检查健康与就绪）：

```bash
docker buildx build --platform linux/amd64 -t manjuflow-studio:amd64 --load .
docker buildx build --platform linux/arm64 -t manjuflow-studio:arm64 --load .
docker image inspect manjuflow-studio:amd64 --format '{{.Os}}/{{.Architecture}}'
docker run -d -p 18080:8080 manjuflow-studio:amd64
curl http://127.0.0.1:18080/healthz
curl http://127.0.0.1:18080/readyz
```

## 时间与时区

业务时区固定为 `Asia/Shanghai`：渲染日配额按业务日归集，工坊报名与提交按窗口边界判定。镜像内嵌 `time/tzdata`，
不依赖系统 zoneinfo。
