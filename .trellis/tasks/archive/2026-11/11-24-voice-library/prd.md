# 语音音色库：独立 voices 表 + 上游导入 + 管理页 + 动态列表

## Goal

在网关后台建立可复用的音色库，支持**从上游直接导入音色（ID + 名字）**，并对 Infinite Canvas 暴露动态音色列表，取代其硬编码的 13 个 OpenAI 音色。

## 前提变更（重要）

本任务最初按「canvas 零改动 + 13 槽位劫持」设计，需求方后续确认**可以修改 canvas 前端**。因此：

- 原设计中的 `canvas_slot` 列与部分唯一索引**作废**，不进入实现。
- 音色数量不再受 13 个限制，画布显示真实音色名而非 `Alloy`/`Ash`。
- 方案核心变为：上游导入 → 本地库筛选 → canvas 动态拉取。

## 已核实的上游能力（非推测）

| Provider | 拉取音色列表 | 接口 | 字段 |
|---|---|---|---|
| ElevenLabs | 支持 | `GET /v1/voices`（`xi-api-key`） | `voices[].voice_id` + `voices[].name` |
| Fish Audio | 支持 | `GET /model?self=true&page_size=100`（Bearer） | `items[]._id`（即 `reference_id`）+ `items[].title` |
| fal | **不支持** | 无音色库概念，`voice` 是模型自有字段 | — |

- ElevenLabs 代码里已有对 `/v1/voices` 的调用（`firstVoice`），但只取 `voices[0].voice_id` 丢弃了 `name`，需扩展。
- Fish Audio 官方文档确认端点为 `GET /model`；搜索结果中出现的 `GET /v1/model/list-models` 与官方 OpenAPI 不符，**不采用**。

## Requirements

### R1 数据层

- 新增迁移 `007_voices.sql`（不改已发布的 001~006）：

  | 列 | 类型 | 说明 |
  |---|---|---|
  | `id` | TEXT PK | `vc_` + `RandHex(6)` |
  | `name` | TEXT NOT NULL | 显示名，可由上游 `title`/`name` 导入后本地改写 |
  | `provider_id` | TEXT NOT NULL | 归属平台 |
  | `upstream_id` | TEXT NOT NULL | 上游真实音色 ID（`voice_id` / `reference_id`） |
  | `options` | TEXT NOT NULL DEFAULT '{}' | 预留 |
  | `enabled` | INTEGER NOT NULL DEFAULT 1 | 仅 enabled 音色对 canvas 可见 |
  | `created_at` / `updated_at` | INTEGER NOT NULL | unix-ms |

- 索引 `idx_voices_provider(provider_id)`。
- **唯一约束**：`UNIQUE(provider_id, upstream_id)`，避免同一平台重复导入同一音色。
- `DeleteProvider` 的子表计数需加上 `voices`（否则 FK 报错变 500，而非预期 409）。

### R2 上游音色拉取

- 在 `internal/provider/provider.go` 仿 `ModelLister` 增加：

  ```go
  type UpstreamVoice struct {
      ID     string `json:"id"`
      Name   string `json:"name"`
      Detail string `json:"detail,omitempty"` // 如语言、状态
  }
  type VoiceLister interface {
      ListVoices(ctx context.Context, c Call) ([]UpstreamVoice, error)
  }
  ```

- `elevenlabs` 实现：读 `voice_id` + `name`，复用现有 `defaultVoiceCache` 的缓存思路（TTL 10 分钟）。
- `fishaudio` 实现：调 `GET /model?self=true&page_size=100`，过滤 `type=="tts"` 与 `state=="trained"`，取 `_id` + `title`。
- `fal` **不实现**该接口，admin 层按类型断言失败给出明确提示「该平台不支持拉取音色，请手动填写」。

### R3 Admin API

- 新增 `internal/admin/voices.go`：
  - `GET /admin/voices?provider_id=` → `{"voices":[...]}`
  - `POST /admin/voices` → 201
  - `PATCH /admin/voices/{id}` → 200
  - `DELETE /admin/voices/{id}` → 204
- 新增上游导入探测端点（复用 `upstreamModels` 的账号挑选 / 代理解析 / `scrubKey` 范式）：
  - `GET /admin/providers/{id}/upstream-voices` → `{"data":[UpstreamVoice...]}`
- 校验全部返回 400：`name` 非空且 ≤64；`provider_id` 存在；`upstream_id` 非空且 ≤256；`(provider_id, upstream_id)` 重复 → 409。

### R4 对 canvas 暴露音色列表

- 新增 `GET /v1/audio/voices`，返回当前全部 `enabled=1` 的音色：

  ```json
  {"object":"list","data":[{"id":"vc_ab12cd","name":"中文旁白","provider_id":"elevenlabs"}]}
  ```

- 走现有 gateway key 鉴权（`/v1/` 子树）；CORS 已全局放开（含 OPTIONS 预检），无需改动。
- 不返回 `upstream_id`——canvas 只发 `id`，由后台映射成上游 ID，避免上游凭据/ID 外泄。

### R5 音色解析与二次映射

- `internal/openai/speech.go`：`accounts.Resolve` 之后查库，请求的 `voice` 命中 `voices.id`（按当前模型所属平台 + enabled）则替换为 `upstream_id`。
- 加 `media.SpeechRequest.VoiceResolved bool`，三个适配器改为「已解析则跳过 `ResolveVoice`」：

  ```go
  voice := r.Voice
  if !r.VoiceResolved {
      voice = provider.ResolveVoice(c, voice)
  }
  ```

- 优先级：**库命中 > options 别名 > 原样透传**，三者须有单测。
- 库为空时 `speech.go` 行为与当前完全一致（回归保护）。

### R6 管理页

- 新增 `internal/web/assets/pages/voices.js` + `app.js` 路由 `#/voices`（「模型」分组）。
- 列表：显示名 / 平台 / 上游 ID / 状态 / 操作。
- 表单：显示名 / 平台下拉 / 上游 ID / 启用。
- **「从上游导入」**：按钮触发 `GET /admin/providers/{id}/upstream-voices`，弹出勾选列表，批量写入 `voices`。fal 平台显示「不支持拉取，请手动填写」。

### R7 canvas 前端

- `web/src/lib/audio-generation.ts`：`normalizeAudioVoiceValue` 放开未知值（不再强制回落 `alloy`）；`audioVoiceOptions` 降级为兜底。
- 新增 `fetchAudioVoices(config)`，仿现有 `fetchImageModels`（`services/api/image.ts`）的 `GET /models` 写法调 `GET /v1/audio/voices`。
- `components/audio-settings-panel.tsx`、`components/layout/app-config-modal.tsx` 改为动态选项；拉取失败时回落到内置 `audioVoiceOptions`。

### R8 不做

- 不改 `/v1/audio/speech` 的请求/响应形状。
- 不引入级联删除。
- 不把音色暴露成 `/v1/models` 条目。

## Acceptance Criteria

- [ ] `go build ./cmd/gateway`、`go vet ./...`、`go test ./...` 全绿。
- [ ] 全新 DB 建出 `voices` 表；已有 DB 从 006 平滑升级。
- [ ] `/admin/voices` CRUD 走通；非法输入 400、重复上游 ID 409、未知 id 404。
- [ ] `GET /admin/providers/{id}/upstream-voices` 对 ElevenLabs 与 Fish Audio 返回真实列表（需真实 key 实测，未实测前不得声称通过）。
- [ ] fal 平台的导入探测端点返回明确的「不支持」400。
- [ ] 删除仍被音色引用的平台返回 409，且数据未被删除。
- [ ] `GET /v1/audio/voices` 只返回 `enabled=1` 的音色，且不含 `upstream_id`。
- [ ] 解析优先级三档各有用例：库命中 / options 别名 / 原样透传。
- [ ] 库为空时语音行为与改动前一致（回归用例）。
- [ ] canvas 拉取音色失败时回落到内置列表，不白屏、不报错中断。
- [ ] `Infinite Canvas.md` 未被修改。

## Notes

- `Infinite Canvas.md` 是客户端契约源，按 `directory-structure.md` 约定不得编辑。
- Fish Audio 端点已由官方 OpenAPI 文档确认；但**实际响应仍需用真实 key 验证**后再宣布该功能可用。
- 音色库存在的意义是「可控」：上游名只是导入初值，你可以在库里改成本地化名字，也可以只暴露部分音色给 canvas。
