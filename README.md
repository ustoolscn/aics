# AICS Feishu Customer Bot

一个后端运行的 Go 客服机器人骨架：连接飞书机器人，在飞书话题群中把每个话题当作独立对话，通过大模型、工具和知识库完成回复。

## 核心模型

```text
一个飞书话题 = 一个客服会话 = 一个独立上下文
```

Bot 收到消息后会根据 `chat_id + root_message_id/thread_id` 定位会话。不同话题的历史、摘要、工具结果互不污染。

## 快速开始

1. 准备配置：

```powershell
Copy-Item .env.example .env
```

2. 修改 `.env`：

```text
FEISHU_APP_ID=cli_xxx
FEISHU_APP_SECRET=xxx
LLM_API_KEY=sk-xxx
LLM_BASE_URL=https://api.openai.com/v1
LLM_MODEL=gpt-4.1-mini
```

3. 启动：

```powershell
go run ./cmd/aics
```

默认使用飞书长连接模式，不需要公网回调地址。

## 目录

```text
cmd/aics                  启动入口
configs/customer_service.md 客服身份和行为提示
internal/app              依赖装配
internal/config           配置
internal/feishu           飞书事件和回复
internal/llm              OpenAI-compatible 大模型客户端
internal/orchestrator     对话编排
internal/session          话题会话存储
internal/feishuhistory    从飞书话题拉取历史上下文
internal/tool             工具接口和知识库工具
```

## 当前能力

- 飞书长连接接收消息事件
- 收到消息后先添加 ACK 表情
- 流式输出：先回复占位消息，再持续更新内容
- 消息去重：同一个飞书消息事件只处理一次
- 图片消息：下载飞书图片，上传到图床，再以 vision 消息交给大模型
- 每个话题独立 session
- 加载客服身份提示文件
- 最近多轮上下文：支持内存 session 或从飞书话题实时拉取
- OpenAI-compatible Chat Completions 调用
- 工具调用协议预留
- 本地 Markdown 知识库检索工具
- PostgreSQL/pgvector + Gemini Embedding RAG 知识库检索
- 回复到收到的飞书消息下

## ACK 表情

收到用户消息后，服务会先给原消息添加一个表情，让用户知道机器人已经接到请求。

```text
ACK_REACTION_EMOJI=OK
```

如果飞书后台没有开放表情回复权限，或表情类型不被当前租户支持，服务会记录 warning，并继续生成客服回复。

## 流式输出和去重

```text
LLM_STREAM=true
STREAM_UPDATE_INTERVAL_MS=800
MESSAGE_DEDUPE_TTL_SECONDS=3600
DEDUPE_STORE=redis
REDIS_CONN_STRING=redis://:password@127.0.0.1:6379/0
```

开启流式后，Bot 会先在原话题下回复“正在处理...”，随后用飞书消息更新接口持续刷新这条回复。为了避免飞书重试导致重复回复，服务会按 `message_id` 做本地去重。

`DEDUPE_STORE=memory` 时使用进程内存去重；`DEDUPE_STORE=redis` 时使用 Redis `SET NX EX` 做短期幂等去重，适合服务器和多实例部署。Redis 不保存聊天历史，也不会写入知识库。

## 会话历史

```text
MAX_HISTORY_MESSAGES=20
HISTORY_SOURCE=feishu
FEISHU_HISTORY_LOOKBACK_HOURS=168
```

`HISTORY_SOURCE=memory` 时，服务在进程内保存最近对话，适合本地开发。`HISTORY_SOURCE=feishu` 时，服务每次从飞书当前话题拉取最近消息作为上下文，不长期保存聊天历史；如果飞书消息列表接口还没返回当前消息，服务会把当前用户消息补进上下文。

飞书话题历史使用 `container_id_type=thread` 和当前 `thread_id` 获取；话题容器不支持 `start_time` / `end_time` 时间范围过滤。

## 图片消息

```text
IMAGE_HOST_UPLOAD_URL=https://2bad.lujilujilujilujiluji.com/
IMAGE_HOST_AUTH_HEADER=Authorization
IMAGE_HOST_AUTH_VALUE=Bearer cooper
IMAGE_HOST_FIELD_NAME=file
IMAGE_HOST_RESPONSE_URL_PATH=url
```

`IMAGE_INPUT_MODE=base64` 时，服务会先从飞书下载图片资源，再转为 `data:image/...;base64,...` 交给大模型。设为 `upload` 时，会 multipart 上传到图床，图床返回公网 URL 后再发送给大模型。

## RAG 知识库

默认仍可使用本地 Markdown/TXT 关键词检索。配置 `DATABASE_URL` 和 `EMBEDDING_API_KEY` 后，`KNOWLEDGE_MODE=auto` 会自动切换到 PostgreSQL/pgvector RAG 检索。

```text
KNOWLEDGE_MODE=auto
KNOWLEDGE_DIR=knowledge
DATABASE_URL=postgres://user:pass@localhost:5432/aics?sslmode=disable
EMBEDDING_BASE_URL=https://example.com/v1beta
EMBEDDING_API_KEY=xxx
EMBEDDING_MODEL=gemini-embedding-2
EMBEDDING_DIMENSIONS=768
KNOWLEDGE_TOP_K=5
KNOWLEDGE_CHUNK_SIZE=900
KNOWLEDGE_CHUNK_OVERLAP=120
```

索引知识库：

```powershell
go run ./cmd/aics-index
```

索引命令会读取 `KNOWLEDGE_DIR` 下的 `.md` 和 `.txt` 文件，切成 chunk，使用 `gemini-embedding-2` 生成 768 维向量，并写入 `knowledge_chunks` 表。线上 Bot 查询时仍然调用同一个 `knowledge_search` 工具名，只是内部改为向量检索。

生产镜像不打包 `knowledge/` 目录；RAG 模式下线上 Bot 只查 PostgreSQL/pgvector。Markdown 源文件建议保留在仓库或管理端，更新后重新运行索引命令写入数据库。

`EMBEDDING_BASE_URL` 留空时会直连 Google Gemini 官方接口；使用第三方中转时，程序会请求：

```text
{EMBEDDING_BASE_URL}/models/{EMBEDDING_MODEL}:embedContent
```

## 后续生产化建议

- 增加转人工、查订单、创建工单等真实业务工具
- 增加管理端，用于维护知识库 Markdown、触发 RAG 索引和查看未命中问题
- 增加部署文档，覆盖 webhook 域名、Redis、PostgreSQL/pgvector、GHCR 镜像运行方式
