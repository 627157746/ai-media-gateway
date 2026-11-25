# Design：画布按网关能力参数按需显示设置项

代码改动都在画布仓库 `infinite-canvas/`（下文路径相对它）。网关仓库本任务不改代码。

## 1. 渠道协议 `aimg`

- 前端 `web/src/lib/model-channel.ts`：`modelChannelProtocols` 增加 `{ value: "aimg", label: "AI Media Gateway", baseUrl: "" }`。不设 `directRequestPlan`，请求走 OpenAI 兼容通道。
- 后端 `service/model_protocol.go`：新增常量 `ModelChannelProtocolAIMG = "aimg"`，加入 `modelProtocolIDs`，用 `compatible` 适配器（Bearer 鉴权、OpenAI URL 拼接）。
- Base URL 由用户填写，例如 `http://host:8090/v1`。拼接规则沿用 `buildApiUrl`（实现时核对是否会重复拼 `/v1`）。

## 2. 能力参数的数据结构与存储

新文件 `web/src/lib/ai-media-gateway.ts`，放类型和工具函数：

```ts
export type GatewayModelSpec = { kind: "image" | "video" | "speech"; capabilities: GatewayImageCaps | GatewayVideoCaps | GatewaySpeechCaps };
export type GatewayModelSpecs = Record<string, GatewayModelSpec>;
```

字段名与网关 JSON 完全一致（snake_case），不做改名映射，见 PRD「现状」一节。

存储：渠道上新增 `modelSpecs` 字段，与现有 `modelCapabilities`（模型分类）并列。

| 位置 | 改动 |
|---|---|
| 本地渠道 `LocalModelChannel`（`web/src/stores/use-config-store.ts`） | 加 `modelSpecs?: GatewayModelSpecs` |
| 后台私有渠道 `model.ModelChannel`（`model/setting.go`） | 加 `ModelSpecs map[string]json.RawMessage \`json:"modelSpecs,omitempty"\``。内容不解析，原样存 |
| 公共渠道 `model.PublicModelChannelInfo` + 前端 `publicChannels` 类型 | 加同名字段；`service/settings.go` 生成公共渠道时按已启用模型过滤后下发，写法同 `ModelCapabilities` |

后台设置整体以 JSON 存储，不新增数据表（实现时核对；若有表结构变化需更新 `docs/backend/backend-database.md`）。

## 3. 获取模型

`kind` 映射到画布分类：`image→image`、`video→video`、`speech→audio`。获取到的模型自动写入 `modelCapabilities`，用户不用再手动分类。

- **本地渠道**：`web/src/services/api/image.ts` 新增 `fetchGatewayModelSpecs(config)`，请求 `GET {baseUrl}/models`，返回 `{ id → { kind, capabilities } }`。`app-config-modal.tsx` 的 `fetchLocalModelList` 遇到 `aimg` 渠道时调用它，把结果暂存在组件 state 里，返回 `Object.keys(...)` 给选择弹窗。`confirmLocalModelSelector` 保存时，只保留选中模型的 `modelSpecs`，并用 `kind` 补齐这些模型的 `modelCapabilities`。
- **后台渠道**：Go 新增 `fetchGatewayChannelModelSpecs(channel) (map[string]json.RawMessage, error)`，以及接口 `POST /api/admin/settings/channel-model-specs`（入参和 `channel-models` 一样），返回 `{ 模型名: {kind, capabilities} }`。前端 `admin.ts` 新增 `fetchChannelModelSpecs`。后台设置页遇到 `aimg` 渠道只调这个接口（模型名取 key，上游只请求一次），确认时把 `modelSpecs` 和分类写进渠道表单。现有 `channel-models` 接口不动。
- 只在点「获取模型」时更新。网关改了配置，需要重新获取一次。

## 4. 查找当前模型的能力参数

`ai-media-gateway.ts` 提供：

```ts
export function gatewaySpecForConfig(config: AiConfig, model: string): GatewayModelSpec | null
```

- 本地模式：`localChannelForActiveModel({ ...config, model })`，要求 `protocol === "aimg"` 且 `modelSpecs[model]` 存在。
- 远程模式：在 `config.publicChannels` 里按 `channelIdForActiveModel` 找渠道，条件同上。
- 找不到就返回 `null`，调用方走原有逻辑，保证其他渠道行为不变（R5）。

## 5. 面板（按能力参数渲染）

新文件 `web/src/components/gateway-settings.tsx`，导出 `GatewayVideoSettings`、`GatewayImageSettings`、`GatewayAudioSettings`。复用现有的 `SettingGroup`、`OptionPill`、`ImageSettingsTheme` 和画布主题 token，不硬编码颜色。三个原面板在开头加一行判断：`const gw = gatewaySpecForConfig(...)`，有值就直接 `return` 网关版面板，放在所有按模型名判断的分支（Seedance、Kling、Grok 等）**之前**。

### 视频

| 能力字段 | 界面 | 写入的配置键 |
|---|---|---|
| `modes` 多于 1 个 | 「首尾帧 / 参考图」切换 | `videoMode`（`frames` / `reference`） |
| `resolutions` | 清晰度选项，默认值取 `resolution_default` | `vquality` |
| `ratios` | 比例选项，默认值取 `ratio_default` | `size`（存比例值，如 `16:9`） |
| `durations` 或 `duration_min`/`duration_max` | 时长选项或滑块，默认值取 `duration_default` | `videoSeconds` |
| `supports_negative_prompt` | 负面提示词输入框 | `videoNegativePrompt` |
| `supports_audio` | 音频生成开关 | `videoGenerateAudio` |

字段为空就不显示对应设置项。当前值不在选项里时，显示默认值（不偷偷改配置）。

首尾帧、参考图、参考视频、参考音频的上传入口由 `supports_first_frame`、`supports_last_frame`、`max_references`、`max_reference_videos`、`max_reference_audios` 决定。目前 `web/src/app/(user)/video/page.tsx` 用 `supportsVideoFrameReferences` / `supportsVideoAudioGeneration` 判断，这里加网关分支。画布节点的上传入口在哪判断，实现时查清后同样处理。

### 图片

| 能力字段 | 界面 | 配置键 |
|---|---|---|
| `qualities` | 质量选项 | `quality` |
| `sizes` + `size_mode` | `size_mode=aspect_ratio` 显示比例选项，否则显示尺寸选项；只列 `sizes` 里的值 | `size` |
| `max_n` | 数量上限，`max_n<=1` 时隐藏数量 | `count` |
| `backgrounds` | 背景选项 | 新键 `imageBackground` |
| `formats` | 输出格式选项 | 新键 `imageFormat` |
| `defaults` | 各项的默认值 | — |

`ImageSettingsPanel` 的 `onConfigChange` 类型要放宽，加上两个新键。实现时先核对调用方是不是通用 setter；不是的话，只改调用方的类型声明。

### 语音

| 能力字段 | 界面 | 配置键 |
|---|---|---|
| `voice_mode=preset` | 下拉框：`voice_presets`，默认值取 `default_preset` | `audioVoice` |
| `voice_mode=library` / `sample` | 下拉框：`voice_presets` 加上 `GET {baseUrl}/audio/voices?model=` 返回的音色库 | `audioVoice` |
| `voice_mode=prompt` | 音色描述输入框 | `audioVoice` |
| `voice_mode=none` | 不显示 | — |
| `formats` | 格式 | `audioFormat` |
| `language.supported` | 语言下拉框，选项取 voices 接口返回的 `languages` | 新键 `audioLanguage` |
| `speed.mode` 是 `field` / `prompt` | 语速滑块，范围 `min`–`max` | `audioSpeed` |
| `instructions_uses` 非空 | 声音指令输入框 | `audioInstructions` |

音色列表用 `useGatewayVoices(config, model)` hook（放在 `gateway-settings.tsx`）获取：本地模式用渠道的 baseUrl 和 apiKey 直接请求；远程模式走画布后端代理。画布后端没有对应代理的话，新增 `GET /api/v1/audio/voices`，转发到当前 `aimg` 渠道（实现时核对现有 `/api/v1/*` 代理是怎么做的）。

## 6. 生成请求（R4：只发送支持的参数）

每个请求函数开头都加网关分支，放在所有按模型名判断的分支之前：

- **视频** `createVideoRequestBody`：新增 `createGatewayVideoRequestBody`，返回 FormData，字段如下：
  - 必填：`model`、`prompt`
  - 有对应能力时才加：`seconds`、`ratio`、`resolution_name`
  - `modes` 多于 1 个时才加：`mode`
  - 支持时才加：`negative_prompt`、`video_generate_audio`
  - 素材按上限截取：`input_reference[]`（≤`max_references`）、`video_reference[]`、`audio_reference[]`
  - 支持时才加：`first_frame_url`、`last_frame_url`
  - `createVideoGenerationTask` 里还有几处按模型名判断的逻辑：建任务的 URL、Seedance 草稿、轮询 id、解析返回结果。网关模型一律走 `/videos` 和通用逻辑，不进入这些分支。
- **图片**：在 `requestImageGenerationSingle` 和 `requestImageEditSingle` 的入口加网关分支，入口的确切位置实现时确认。规则：
  - `size`、`quality`、`background`、`output_format` 只在值落在能力列表里时才发
  - `n` 不超过 `max_n`
  - `supports_edit=false` 但带了参考图，或 `requires_images=true` 但没带图时，请求前直接报中文错误
- **语音** `buildAudioSpeechRequest`：
  - 必填：`model`、`input`
  - 按模式发 `voice`
  - `response_format` 只在值落在 `formats` 里时才发
  - `speed` 只在 `speed.mode` 不是 `none` 时才发
  - 支持时才加：`instructions`、`language`

远程模式的请求经画布后端 `/api/v1/*` 转发。实现时要核对：转发时 multipart 字段是不是原样透传。

## 7. 不在本次范围

- 能力参数自动刷新
- 网关侧的任何改动
- 流式出图
- 画布其他渠道的行为调整

## 8. 风险

- 画布的按模型名判断散落在很多地方，如果漏掉一处，网关模型可能被误判成厂商专用格式。缓解办法：每个入口都把网关分支放在最前面；实现时用 `rg` 搜一遍 `modelKey(`、`is*Model(`、`is*Config(` 的调用点，逐个确认。
- 远程模式的音色列表和请求转发依赖画布后端代理，具体行为还没核实。
