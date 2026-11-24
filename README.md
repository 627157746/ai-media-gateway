# AI Media Gateway

一个 OpenAI 兼容的 AI 媒体网关（Go 编写）。上游对接 fal / ElevenLabs / Fish Audio 等媒体生成服务，对外提供 OpenAI 风格的图片生成 / 编辑、视频生成（异步任务）、语音合成接口，供 Infinite Canvas 等客户端直接使用。

核心能力：

- **多账户池**：同一供应商下可挂多个 API Key，按优先级分组 + 平滑加权轮询（SWRR）分配请求。
- **故障转移与冷却**：上游 401/402/429/5xx 等错误自动换账户重试；按错误类别分别设置冷却时间（限速短冷却、余额不足长冷却、认证错误超长冷却），支持指数退避。
- **任务亲和**：视频等异步任务提交后绑定到提交成功的账户，后续轮询始终走同一账户。
- **密钥加密**：账户 API Key 使用 `MASTER_KEY`（AES-256-GCM）加密存储，管理接口只返回掩码。
- **请求日志**：每次 `/v1/*` 请求记录模型/供应商/账户/重试次数/耗时，可通过管理接口查询。

## 运行

### 本地运行

需要 Go 1.27+。

```powershell
$env:ADMIN_TOKEN = "your-admin-token"
$env:MASTER_KEY  = "your-master-key"
go run ./cmd/gateway
# 或先构建： go build -o bin/gateway.exe ./cmd/gateway; ./bin/gateway.exe
```

### Docker

```bash
cp .env.example .env   # 填入 ADMIN_TOKEN 和 MASTER_KEY
docker compose up -d --build
```

数据库存放在 `./data/gateway.db`（容器内 `/data`）。

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ADMIN_TOKEN` | （必填） | 管理 API `/admin/*` 的 Bearer token |
| `MASTER_KEY` | （必填） | 账户密钥加密主密钥（任意字符串，内部取 SHA-256 作 AES 密钥） |
| `LISTEN_ADDR` | `:8080` | 监听地址 |
| `DB_PATH` | `data/gateway.db` | SQLite 数据库路径（父目录自动创建） |
| `MAX_BODY_MB` | `100` | 请求体大小上限（MB） |
| `IMAGE_TIMEOUT` | `5m` | 图片/语音等同步生成任务的上游等待超时 |
| `HTTP_TIMEOUT` | `5m` | 单次上游 HTTP 调用超时 |
| `LOG_RETENTION` | `720h` | 请求日志保留时长（每小时清理一次） |
| `CORS_ORIGINS` | `*` | 允许的跨域来源，逗号分隔 |
| `COOLDOWN_RATELIMIT` | `60s` | 429 限流冷却（优先使用上游 Retry-After） |
| `COOLDOWN_UPSTREAM` | `30s` | 上游 5xx 冷却基数（按 fail_count 指数退避） |
| `COOLDOWN_UPSTREAM_MAX` | `10m` | 上游冷却上限 |
| `COOLDOWN_QUOTA` | `1h` | 余额不足 / 配额冷却 |
| `COOLDOWN_AUTH` | `24h` | 认证失败冷却 |
| `MAX_ATTEMPTS` | `0` | 单请求最大尝试账户数（0 = 不限，直到无可用账户） |

## 管理 API 快速上手

以下示例假设网关跑在 `http://localhost:8080`，`ADMIN_TOKEN=admintoken`。

### 1. 创建网关访问密钥（客户端用）

```bash
curl -X POST http://localhost:8080/admin/keys \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"name":"canvas"}'
# → {"id":"...","name":"canvas","key":"sk-mg-..."}   之后可在 API Key 页随时复制
```

### 2. 注册供应商与账户

fal，挂两个账户（优先级/权重控制调度）：

```bash
curl -X POST http://localhost:8080/admin/providers \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"id":"fal","type":"fal","base_url":"https://queue.fal.run"}'

curl -X POST http://localhost:8080/admin/providers/fal/accounts \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"name":"fal-main","api_key":"fal-key-1","priority":0,"weight":3}'

curl -X POST http://localhost:8080/admin/providers/fal/accounts \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"name":"fal-backup","api_key":"fal-key-2","priority":0,"weight":1}'
# 同优先级组内按 3:1 轮询；不同 priority 时高值组先被尝试
```

ElevenLabs / Fish Audio：

```bash
curl -X POST http://localhost:8080/admin/providers \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"id":"el","type":"elevenlabs"}'
curl -X POST http://localhost:8080/admin/providers/el/accounts \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"api_key":"el-key","options":{"default_voice":"Rachel"}}'

curl -X POST http://localhost:8080/admin/providers \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"id":"fish","type":"fishaudio"}'
curl -X POST http://localhost:8080/admin/providers/fish/accounts \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"api_key":"fish-key"}'
```

### 3. 注册模型

```bash
# fal 视频模型（文生/图生走不同上游端点，时长参数为 "5s" 格式）
curl -X POST http://localhost:8080/admin/models \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{
    "id":"seedance-2.5","provider_id":"fal","kind":"video",
    "upstream":"bytedance/seedance-2.5/text-to-video",
    "options":{
      "upstream_image":"bytedance/seedance-2.5/image-to-video",
      "fields":{"duration_format":"{n}s"}
    }}'

# fal 图片模型（带编辑端点）
curl -X POST http://localhost:8080/admin/models \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"id":"flux-pro","provider_id":"fal","kind":"image",
       "upstream":"fal-ai/flux-pro/v1.1",
       "options":{"upstream_edit":"fal-ai/flux-pro/v1.1/edit"}}'

# ElevenLabs 语音（voices 别名映射：对外名字 → 上游 voice_id）
curl -X POST http://localhost:8080/admin/models \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"id":"eleven-v3","provider_id":"el","kind":"speech","upstream":"eleven_v3",
       "options":{"voices":{"narrator":"21m00Tcm4TlvDq8ikWAM"},"default_voice":"narrator"}}'

# Fish Audio 语音
curl -X POST http://localhost:8080/admin/models \
  -H "Authorization: Bearer admintoken" -H "Content-Type: application/json" \
  -d '{"id":"fish-s2","provider_id":"fish","kind":"speech","upstream":"s2"}'
```

### 4. 模型直通（passthrough）

供应商 `options` 里 `passthrough` 缺省为 `true`：客户端请求 `fal/<任意 fal 端点>`（如 `fal/fal-ai/veo3/fast`）时，未注册别名的模型名会按第一个 `/` 切分，直接以斜杠后半部分作为上游端点调用 fal。注册别名后别名优先。

## 多账户行为

- **选择**：每次请求按 priority 降序分组，组内按权重做平滑加权轮询；前一组的所有账户尝试过（或可转移的错误发生后）才会落到下一组。
- **故障转移**：认证失败 / 配额耗尽 / 限流 / 上游 5xx 会自动换账户重试；参数错误、内容审核拒绝等不重试。
- **冷却**：失败账户按错误类别冷却（见环境变量表），冷却后自动恢复；成功后计数清零。
- **任务亲和**：`/v1/videos` 提交成功后，任务与使用的账户绑定，`GET /v1/videos/{id}` 轮询始终走该账户；账户被删除则任务标记失败。
- 观察与干预：

```bash
curl http://localhost:8080/admin/status -H "Authorization: Bearer admintoken"   # 各账户 state/cooling/计数
curl -X POST http://localhost:8080/admin/accounts/<id>/reset -H "Authorization: Bearer admintoken"  # 手动复位
curl "http://localhost:8080/admin/logs?limit=50" -H "Authorization: Bearer admintoken"              # 请求日志
curl "http://localhost:8080/admin/tasks?status=queued" -H "Authorization: Bearer admintoken"        # 视频任务
```

## 代理

网关支持三级代理配置，**优先级：账号代理 > 平台代理 > 全局代理**，留空表示继承上一级：

- **账号代理**：平台卡片上某个 key 的「编辑 → 更多设置 → 代理」，只影响这一个 key。
- **平台代理**：平台的「编辑 → 代理」，影响该平台下所有未单独设置代理的 key。
- **全局代理**：「设置」页（或 `PATCH /admin/settings` 的 `proxy_url`），影响所有未单独设置的平台/账号。

取值：`留空` = 继承上一级；`direct` = 直连（不走任何代理，可覆盖上一级配置）；代理池引用（在设置里选已保存的代理）；或代理 URL，支持 `http` / `https` / `socks5` / `socks5h`，可带 `user:pass@` 认证。全部为留空时沿用 `HTTPS_PROXY` 等系统环境变量。界面上的「测试」按钮会实际探测连通性。

**代理池**：「设置 → 代理池」里保存的命名代理，三级配置里都能直接下拉选用；改池里的地址，所有引用立即生效。正在被引用的代理不能删除（409 会列出占用方）。

```bash
# 例：让 fal 平台的所有请求走本地 Clash
curl -X PATCH http://localhost:8080/admin/providers/fal -H "Authorization: Bearer admintoken" \
  -d '{"proxy_url":"http://127.0.0.1:7890"}'
# 例：全局走 socks5，但某个 key 直连
curl -X PATCH http://localhost:8080/admin/settings -H "Authorization: Bearer admintoken" \
  -d '{"proxy_url":"socks5://127.0.0.1:1080"}'
curl -X PATCH http://localhost:8080/admin/accounts/<id> -H "Authorization: Bearer admintoken" \
  -d '{"proxy_url":"direct"}'
```

## 客户端调用

### Infinite Canvas

- 渠道类型：**OpenAI**
- `baseUrl`：`http://<host>:8080`（**不要**带 `/v1`，Canvas 会自己拼 `/v1`）
- `apiKey`：上面第 1 步创建的 `sk-mg-...` 网关密钥
- 模型：填注册的模型 id（如 `seedance-2.5`、`flux-pro`、`eleven-v3`），或直通名 `fal/<fal 端点>`
- Canvas 按模型名猜测能力类型（名字含 video/tts/image 等关键词），不读取 `/v1/models` 的 `kind`；「从平台获取」导入时会自动补 `-video` / `-tts` / `-image` 后缀，识别不对时在 Canvas 渠道编辑里手动切换能力即可。

### 接口一览

`/v1/*` 端点除 `sk-mg-*` 网关密钥外也接受 `ADMIN_TOKEN`（内置「试用」页面就是这么鉴权的）；客户端仍应使用 `sk-mg-*` 密钥。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 健康检查（无需认证） |
| GET | `/v1/models` | 已启用模型列表 |
| POST | `/v1/images/generations` | 文生图（JSON） |
| POST | `/v1/images/edits` | 图片编辑（JSON 或 multipart，字段 `image`/`image[]`/`mask`） |
| POST | `/v1/videos` | 视频任务提交（JSON 或 multipart，字段 `first_frame`/`last_frame`/`image[]`/`input_reference`/`video[]`/`audio[]`） |
| GET | `/v1/videos/{id}` | 任务状态轮询（完成时返回 `video_url`） |
| GET | `/v1/videos/{id}/content` | 视频内容流式下载（支持 Range） |
| GET | `/v1/videos?limit=` | 任务列表 |
| POST | `/v1/audio/speech` | 语音合成（流式返回音频） |

## 构建与测试

```powershell
go vet ./...
go test ./...
go build -o bin/gateway.exe ./cmd/gateway
```

唯一第三方依赖是 `modernc.org/sqlite`（纯 Go，无 cgo）。
