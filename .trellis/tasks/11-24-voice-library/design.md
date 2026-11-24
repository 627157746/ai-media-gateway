# 技术设计：语音音色库（上游导入版）

> 取代前一版「13 槽位劫持」设计。`canvas_slot` 列与部分唯一索引已作废。

## 1. 数据流全景

```
                    ┌─────────────────────────────────────────┐
  上游 ElevenLabs   │  GET /admin/providers/{id}/             │
  GET /v1/voices  ──┤    upstream-voices                      │
  上游 Fish Audio   │            ↓ 勾选导入                    │
  GET /model      ──┤  POST /admin/voices                     │
                    │            ↓                            │
                    │      voices 表 (enabled 筛选)            │
                    │            ↓                            │
                    │  GET /v1/audio/voices  ──→ canvas 音色下拉 │
                    │            ↓                            │
  canvas 发 voice=vc_ab12cd                                    
                    │            ↓                            │
                    │  speech.go 查库 → upstream_id            │
                    │            ↓                            │
                    │  adapter.Speak（已解析则跳过 ResolveVoice）│
                    └─────────────────────────────────────────┘
```

## 2. 为什么把 `upstream_id` 藏起来

`GET /v1/audio/voices` 只返回 `{id, name}`，**不返回 `upstream_id`**。

理由：上游音色 ID（尤其 ElevenLabs 的 `voice_id`）对拥有者而言是一种资源标识；把它暴露给浏览器端，任何拿到 gateway key 的客户端都能看到你的私有声音库在什么平台、用什么 ID。canvas 只需要发回 `id`，映射在服务端完成。

这也让「换一个音色提供方」对 canvas 完全透明——换 `upstream_id` 不改前端。

## 3. 唯一约束的选择

```sql
CREATE UNIQUE INDEX idx_voices_upstream ON voices(provider_id, upstream_id);
```

约束在「平台 + 上游 ID」而非全局 `upstream_id`。原因：不同平台的 ID 空间互不相关，ElevenLabs 的 `voice_id` 和 Fish Audio 的 `_id` 理论上可能字符串撞车，全局唯一会造成误判冲突。

重复导入的行为：`CreateVoice` 命中唯一索引 → 返回 `ErrConflict` → 409。管理页的批量导入需要**逐个处理并汇总**「已存在 N 条，跳过」，而不是整批失败。

## 4. 上游拉取的实现细节

### 4.1 接口定义（`internal/provider/provider.go`）

```go
// UpstreamVoice is one voice available upstream, for the 从上游导入 picker.
type UpstreamVoice struct {
    ID     string `json:"id"`
    Name   string `json:"name"`
    Detail string `json:"detail,omitempty"` // 语言 / 状态 / 标签，仅用于展示
}

// VoiceLister is implemented by adapters whose vendor has a voice library.
type VoiceLister interface {
    ListVoices(ctx context.Context, c Call) ([]UpstreamVoice, error)
}
```

**不并行扩展 `ModelLister`**：两者查询对象不同（模型 vs 音色），且 fal 有前者无后者，合并会造成「实现了却不能调用」的假接口。

### 4.2 ElevenLabs

现有 `firstVoice` 打的就是这个端点，但返回结构只留了 `voice_id`：

```go
var out struct {
    Voices []struct {
        VoiceID string `json:"voice_id"`
    } `json:"voices"`
}
```

新增 `ListVoices` 独立读取，扩成：

```go
var out struct {
    Voices []struct {
        VoiceID  string `json:"voice_id"`
        Name     string `json:"name"`
        Category string `json:"category"`
        Labels   map[string]string `json:"labels"`
    } `json:"voices"`
}
```

`Detail` 取 `category`（`premade`/`cloned`/`generated`），这对用户区分「自带音色」和「我克隆的」有用。

缓存：不复用 `defaultVoiceCache`（它按 `base|accountID` 存单个 ID，语义不同），新开一个 `voiceListCache sync.Map`，TTL 沿用 `voiceCacheTTL`。**空列表不缓存**，与 `firstVoice` 现有行为一致（刚创建的音色立刻可见）。

### 4.3 Fish Audio

```go
GET {base|https://api.fish.audio}/model?self=true&page_size=100
Authorization: Bearer <apiKey>
```

响应（官方 OpenAPI 已确认）：

```json
{"total": 3, "items": [{"_id":"...", "type":"tts", "title":"...", "state":"trained", "visibility":"private"}]}
```

过滤条件：

- `type == "tts"`（排除 `svc`）
- `state == "trained"`（`created`/`training`/`failed` 不可用于 TTS）

`Detail` 拼 `visibility`（`private`/`public`/`unlist`），让用户知道哪些是自己私有的。

分页：`page_size=100` 取一页，够用；若 `total > 100`，只在 `Detail` 或响应里标注未取全，不做自动翻页——避免为边缘情况增加复杂度。

### 4.4 fal

不实现 `VoiceLister`。admin 层：

```go
lister, ok := adapter.(provider.VoiceLister)
if !ok {
    fail(w, bad("平台 %s 不支持自动拉取音色，请手动填写上游音色 ID", p.Type))
    return
}
```

## 5. Admin 端点

### 5.1 `GET /admin/providers/{id}/upstream-voices`

直接复用 `upstreamModels`（`admin.go:260`）的完整骨架，差异只有三处：

| 环节 | upstreamModels | upstream-voices |
|---|---|---|
| 适配器断言 | `provider.ModelLister` | `provider.VoiceLister` |
| 调用 | `lister.ListModels(...)` | `lister.ListVoices(...)` |
| 响应 | `{data, next_cursor}` | `{data}`（无分页） |

账号挑选逻辑必须照抄：优先 `!Enabled` 跳过、冷却中的备用、`len(APIKeyEnc)==0` 跳过。这套逻辑已处理「没有可用 key」和「解密失败」，重写会漏掉边界。

错误必须过 `scrubKey(err, call.Account.APIKey)` —— 上游有可能把 key 回显在错误消息里。

### 5.2 CRUD

`internal/admin/voices.go`，风格照 `proxies`：

```go
m.HandleFunc("GET /admin/voices", h.listVoices)
m.HandleFunc("POST /admin/voices", h.createVoice)
m.HandleFunc("PATCH /admin/voices/{id}", h.patchVoice)
m.HandleFunc("DELETE /admin/voices/{id}", h.deleteVoice)
m.HandleFunc("GET /admin/providers/{id}/upstream-voices", h.upstreamVoices)
```

校验表：

| 字段 | 规则 | 违反 |
|---|---|---|
| `name` | 非空，≤64 | 400 |
| `provider_id` | 非空且平台存在 | 400 |
| `upstream_id` | 非空，≤256 | 400 |
| `(provider_id, upstream_id)` | 不重复 | 409 |
| 指定 `id` 时 | 满足 `idRE` | 400 |

平台存在性检查必须显式做（`GetProvider` 返回 `ErrNotFound` → 400 而非 404），否则直接落库会撞 FK 约束变成 500。

## 6. `GET /v1/audio/voices`

`internal/openai/voices.go`：

```go
m.HandleFunc("GET /v1/audio/voices", h.listVoices)
```

```go
type voiceOut struct {
    ID         string `json:"id"`
    Name       string `json:"name"`
    ProviderID string `json:"provider_id"`
}
```

- 查询 `ListVoices(ctx, "")` 后过滤 `Enabled`。
- 响应 `{"object":"list","data":[...]}`，与 `/v1/models` 的形状对齐。
- 按 `name` 排序，让画布下拉稳定。
- 已确认 CORS 中间件对全部路径生效且放行 `GET`，无需改动。

## 7. 解析与二次映射

### 7.1 标记位而非「约定」

前一版想把「库里存的必须是最终上游 ID」当约定，靠文档约束。现在改成代码强制：

```go
// media.SpeechRequest 增加：
VoiceResolved bool // true 表示 Voice 已是上游终值，适配器不得再映射一次
```

三个适配器的改动都是一行守卫：

```go
// elevenlabs
voice := r.Voice
if !r.VoiceResolved {
    voice = provider.ResolveVoice(c, voice)
}

// fishaudio / fal 同理
```

这样「库里存别名」「库里存上游 ID」都不会出现连环映射，用户填什么就是什么。

### 7.2 speech.go 的接入点

```go
res, err := accounts.Resolve(r.Context(), h.st, in.Model, media.KindSpeech)
// ↑ 必须先 Resolve，才能拿到 res.Provider.ID

voiceResolved := false
if in.Voice != "" {
    if v, err := h.st.FindVoice(r.Context(), res.Provider.ID, in.Voice); err != nil {
        httpx.Fail(w, r, err); return
    } else if v != nil {
        in.Voice = v.UpstreamID
        voiceResolved = true
    }
}
sreq := media.SpeechRequest{..., Voice: in.Voice, VoiceResolved: voiceResolved}
```

`FindVoice` 按 `provider_id + enabled=1 + id=?` 查。**只认 `id`，不认 `name`**——名字可改、可重复，用作请求键会导致改名即失效。canvas 拿到的列表里 `value` 就是 `id`。

### 7.3 优先级链路

```
1. voices 表命中（provider 内 enabled）
   → upstream_id，VoiceResolved=true，适配器不再映射
2. provider.ResolveVoice（账号 → 模型 → 平台 options）
   → 命中则替换（兼容老配置）
3. 原样透传
```

## 8. 管理页 `voices.js`

照 `settings.js` 代理池区块的结构。关键差异是导入流程：

```
[新建音色]  [从上游导入 ▾]

点「从上游导入」→ 若未选平台则提示选平台
                → GET /admin/providers/{id}/upstream-voices
                → 弹勾选列表（每行：name  id  detail）
                → 已存在的行置灰并标「已导入」
                → 确定 → 逐条 POST /admin/voices
                → toast「导入 N 条，跳过 M 条已存在」
```

「已导入」判定靠本地对比 `voices` 列表里同平台的 `upstream_id` 集合，不额外请求。

## 9. canvas 前端改动（`D:/project/infinite-canvas/web`）

### 9.1 `src/lib/audio-generation.ts`

```ts
export function normalizeAudioVoiceValue(value: string) {
    const v = (value || "").trim();
    if (!v) return "alloy";
    // 放行任意值：音色改由后台提供，前端不再白名单校验
    return v;
}
```

`audioVoiceOptions` 保留为**兜底列表**，仅当拉取失败时使用。原来的白名单语义被移除——这是本次唯一的破坏性语义变更，会影响三处调用方（面板、popover 标签、config-modal 下拉），但它们都改成读动态列表。

### 9.2 `src/services/api/audio.ts`

仿 `fetchImageModels`（`image.ts:859`）加：

```ts
export async function fetchAudioVoices(config: Pick<AiConfig, "baseUrl" | "apiKey">) {
    const response = await axios.get<{ data?: Array<{ id?: string; name?: string }> }>(
        buildApiUrl(config.baseUrl, "/audio/voices"),
        { headers: { Authorization: `Bearer ${config.apiKey}` } },
    );
    return (response.data.data || [])
        .filter((v): v is { id: string; name: string } => Boolean(v.id))
        .map((v) => ({ value: v.id, label: v.name || v.id }));
}
```

注意：`buildApiUrl` 会自动补 `/v1`，所以 path 写 `/audio/voices` 而非 `/v1/audio/voices`。

### 9.3 面板改造

`audio-settings-panel.tsx`、`app-config-modal.tsx` 的 `audioVoiceOptions` 来源改为「优先动态、失败回落内置」。用一个共享 hook 收敛，避免两处各写一遍拉取逻辑（`useAudioVoiceOptions`，放 `src/lib/` 或 `src/hooks/`）。

拉取失败**必须静默回落**：画布是核心界面，音色列表拉不到不应该阻断用户的其它操作。

## 10. 兼容性与回滚

| 项 | 保证 |
|---|---|
| 迁移 | 007 只新增表/索引，旧 DB 平滑升级 |
| 行为 | `voices` 表为空 → `FindVoice` 恒 nil → `speech.go` 行为与当前完全一致 |
| API | `/v1/audio/speech` 形状不变；新增 `/v1/audio/voices` 是纯增量 |
| 适配器 | `VoiceResolved=false` 时走原逻辑，等于零改动 |
| 回滚点 | `speech.go` 查库那 10 行 + 三个适配器的守卫各一行 |

## 11. 风险与未验证项

| 项 | 说明 |
|---|---|
| Fish Audio 实际响应 | 端点与字段已由官方 OpenAPI 确认，但**未用真实 key 实测**。落地时必须实测，不得仅凭文档宣布可用。 |
| ElevenLabs 免费套餐 | `/v1/voices` 在部分套餐下可能受限或返回空，需在空列表时给出可读提示。 |
| canvas 是外部仓库 | `D:/project/infinite-canvas` 为克隆副本，与上游 `main` 同 commit。改动不提交到原作者仓库，仅本地修改。 |
| 音色名重复 | `name` 不唯一（不同平台的「中文旁白」可共存），canvas 展示时可能重名；`id` 唯一，功能不受影响。 |

## 12. 验证抓手

- `store_test.go`：007 建表、CRUD、(provider_id, upstream_id) 唯一冲突、删平台 409。
- `admin_test.go`：四条 CRUD + 导入端点（用 httptest 假上游注入 `VoiceLister` 实现）+ fal 不支持 400。
- `openai_test.go`：`/v1/audio/voices` 只返回 enabled；解析优先级三档；`VoiceResolved` 阻止二次映射。

---

# 追加：模型能力声明机制（2026-11-24 后续扩展）

> 本任务从「音色库」延伸出的第二部分。PRD 的验收标准（R1–R6）只覆盖音色库，本节记录能力声明的设计与决策，供后续查阅。

## 问题

每个模型支持的参数不同，早期把这些事实硬编码在三处：

1. 适配器里的 `clampSpeed`、`validFormats`
2. `internal/openai/voices.go` 的响应拼装
3. `playground.js` 的 `LANGUAGE_OPTIONS` / `PRESET_VOICES` 表

加一个平台要改三处，且必然漂移。实际发生过两次数据错误：图片尺寸列表和 quality 档位是从经验编的，不是文档里的值。

## 方案：唯一事实来源 + 按需推导

新增 `internal/capability` 包，按媒体类型分三份声明：

| 文件 | 声明 | 来源 |
|---|---|---|
| `voice.go` | `Spec{VoiceMode, Presets, Formats, Language, Speed}` | 各家文档手工整理（音色/语言枚举无接口可取） |
| `image.go` / `image_schema.go` | `ImageSpec{Sizes, MaxN, Qualities, ...}` | **fal OpenAPI 接口**，手写表仅作兜底 |
| `video.go` | `VideoSpec{Modes, Durations, Ratios, ...}` | **fal OpenAPI 接口** |

### 关键决策

**1. fal 能力从接口取，不手写**

`GET https://fal.ai/api/openapi/queue/openapi.json?endpoint_id=<端点>` 匿名可用，返回该端点的真实输入 schema。声明从它推导，新模型零代码接入。

实测验证：新建 `fal-ai/nano-banana/edit`（未写任何参数），自动得到 `sizes=11 个 aspect_ratio 枚举`、`max_n=4`、`size_mode=aspect_ratio`。

**2. schema 优先，手写表兜底**

schema 拉不到（端点写错/上游不可用/非 fal 平台）时退回保守的最小声明，而不是编造。fal 的手写表只覆盖核实过的 `openai/gpt-image-2*`。

**3. 字段语义读 description，不只读字段名**

`image_url`（MiniMax）和 `start_image_url`（阿里）是同一个东西，名字匹配永远不完整。`EndpointField` 保留厂商的 `Description`，双路匹配。

**4. 能力属于端点，不属于平台**

fal 的 `text-to-image` 和 `/edit` 参数不同；MiniMax 的 `image-to-video` 没有 `aspect_ratio` 而 `reference-to-video` 有。因此按模型推导，并合并 `upstream_edit` / `upstream_reference` 兄弟端点：

- 共享枚举**取交集**（避免出现只有一个端点支持的值）
- 兄弟端点独有字段补进来，一律标非必填

**5. 端点选择按请求内容，不只按显式 mode**

`videoEndpoint`：显式 `frames` 优先（不被参考素材劫持）→ 参考素材走 reference → 有图走 image → 否则主端点。

**6. 不声明厂商没有的参数**

Muse 没有时长/比例/分辨率/format，就一个都不声明，前端自动隐藏对应控件。传了这些参数直接 400 并说明原因，而不是静默丢弃。

## 模型级覆盖

`options` 可覆盖推导结果，用于两种情况：

- 端点级差异：`fields.first_frame = "start_image_url"`（wan）
- 部署选择：`requires_images = true`（MiniMax 的 `image_url` 在 schema 里可选，但部署只想做图生视频）

## 动态认证问题

fal 的 `/jobs/<id>/result` 需要 `x-api-key`，而网关的 `/content` 代理用无认证的 `FetchURL` 下载成品。新增可选接口 `provider.VideoFetcher`，由适配器自己带 key 下载，代理只透传。

## 缓存

`/v1/models` 是客户端轮询热点，schema 必须缓存：

- 成功 30 分钟，失败 2 分钟（短暂故障不应锁死半小时）
- 请求 ctx 已取消时不写失败记录
- 读出/写入都 `cloneSchema`，否则合并逻辑会就地改坏缓存，且两个模型共用一个端点时互相污染
- 读 schema 不走 `pool.Do`，避免冷却正在服务生成请求的账号

## 复核发现并修复的缺陷

`trellis-check` 静态复核共修 10 处，4 处阻断：

1. 缓存指针外泄导致数据竞争 + 跨模型污染
2. 读 schema 误记账号成功/失败
3. `videoEndpoint` 显式 `frames` 被参考素材劫持
4. `video_url` / `audio_url` 在非参考端点被错发成数组（fal 要字符串）
5. `*Input` schema 遍历 map 顺序随机
6. 失败缓存 TTL 过长
7. `videoContent` 重复取账号、错误类型被包成 502
8. 前端隐藏行里的旧文件仍会提交
9. 前端切换模型后残留旧值
10. 测试在测副本而非真实函数

## 未完成

- `openai` 平台的图片参数未重新核对（代码内已标注）
- Fish Audio 真实 key 实测（PRD 验收项）
- 视频与图片的手写兜底表只覆盖已核实的端点
