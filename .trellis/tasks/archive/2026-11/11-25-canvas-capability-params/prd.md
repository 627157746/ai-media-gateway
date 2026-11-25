# 画布按网关模型能力参数按需显示设置项

## Goal

在画布 fork（`infinite-canvas/`，远端 `627157746/infinite-canvas`，上游 `tigerowo/infinite-canvas`）里新增「AI Media Gateway」渠道类型。选了这个类型的渠道，画布读取网关 `GET /v1/models` 返回的每个模型的 `capabilities`，视频、图片、语音三个设置面板只显示该模型支持的设置项，选项值也来自能力参数，不再靠画布内置的模型名规则判断。

## 已确认的决定

- 范围：视频、图片、语音三个面板都做。
- 识别方式：新增渠道协议类型（如 `aimg`），只有这个类型启用能力驱动的界面；其他渠道行为不变。
- 渠道：本地渠道（浏览器直连）和后台公共渠道都支持。
- 仓库关系：画布 fork 克隆在网关仓库根目录的 `infinite-canvas/`，已加进网关 `.gitignore`；画布改动在画布仓库里单独提交，推送到 fork 需要另行授权。

## 现状（已查代码）

- 网关 `GET /v1/models` 每项带 `id`、`kind`（image/video/speech）、`capabilities`（`internal/openai/images.go` `listModels`）。
  - 视频：`modes`、`durations`/`duration_min`/`duration_max`/`duration_default`、`ratios`/`ratio_default`、`resolutions`/`resolution_default`、`supports_first_frame`、`supports_last_frame`、`requires_images`、`max_references`、`max_reference_videos`、`max_reference_audios`、`supports_audio`、`supports_negative_prompt`（`internal/capability/video.go`）。
  - 图片：`sizes`、`size_mode`、`max_n`、`qualities`、`resolutions`、`backgrounds`、`formats`、`supports_edit`、`requires_images`、`supports_mask`、`max_edit_images`、`first_image_is_source`、`defaults`（`internal/capability/image.go`）。
  - 语音：`voice_mode`、`voice_presets`、`default_preset`、`formats`、`language`、`speed`、`instructions_uses`、`voice_required`（`internal/capability/voice.go`）。
- 画布渠道协议列表在 `web/src/lib/model-channel.ts` 的 `modelChannelProtocols`；后端 `service/model_protocol.go` 的 `modelProtocolIDs` 也要同步登记。
- 画布获取模型列表：本地渠道走前端 `fetchImageModels`（`web/src/services/api/image.ts`，只取 `id`）；后台渠道走 Go `fetchOpenAIAdminChannelModels`（`service/settings.go`，只取 `id`）。
- 画布已有按模型分类的 `modelCapabilities`（`Record<模型名, "image"|"video"|"text"|"audio">`），本地渠道和后台公共渠道（`PublicModelChannelInfo.ModelCapabilities`）都有存储和下发。
- 三个面板现在靠模型名规则决定显示什么：`web/src/components/video-settings-panel.tsx`（503 行，含 Seedance、Kling 等专用分支）、`image-settings-panel.tsx`、`audio-settings-panel.tsx`；时长、首尾帧、音频生成等判断在 `web/src/lib/video-model-capabilities.ts`。

## Requirements

- R1 新增渠道协议 `aimg`（显示名「AI Media Gateway」），前后端协议表都登记；请求格式走 OpenAI 兼容通道。
- R2 获取模型列表时，`aimg` 渠道同时保存每个模型的能力参数和分类（`kind` 映射到画布的 image/video/audio，不用再手动分类）。
  - 本地渠道：保存到浏览器本地渠道配置。
  - 后台渠道：保存到后台渠道配置，并通过公共渠道信息下发给前端。
- R3 三个面板在当前模型属于 `aimg` 渠道且有能力参数时，按能力参数渲染：没有的设置项不显示，选项列表和默认值来自能力参数。
- R4 生成请求里只发送该模型支持的参数。
- R5 非 `aimg` 渠道的界面和请求行为不变。

## Acceptance Criteria

- [ ] 新建 `aimg` 本地渠道，获取模型后，模型自动按 图片/视频/语音 分类。
- [ ] 后台新建 `aimg` 渠道，获取模型后，普通用户画布里看到同样的分类和设置项。
- [ ] 视频面板：选不同网关模型，时长、比例、清晰度、首尾帧、参考图/视频/音频数量、音频生成开关、负面提示词按能力参数显示或隐藏。
- [ ] 图片面板：尺寸、数量、质量、背景、格式、是否可编辑按能力参数显示。
- [ ] 语音面板：音色（预设/音色库/不需要）、格式、语言、语速、指令按能力参数显示。
- [ ] 其他渠道类型的面板和请求不受影响（回归检查）。
- [ ] 用真实网关联调三类模型各至少一个。

- 能力参数刷新：只在「获取模型」时随渠道配置保存；网关改了模型配置，需要重新点一次「获取模型」才会更新。
- 视频模式：能力参数 `modes` 有多个值时，面板显示「首尾帧 / 参考图」切换，请求带 `mode`；只有一个值时不显示切换。
- 语音音色：下拉框 = 能力参数 `voice_presets` + 网关 `GET /v1/audio/voices` 返回的音色库（`vc_*`）。

## 约束

- 画布 AGENTS.md：任何文件改动前先征得同意；写完不做构建检查；任务完成前同步更新 `docs/progress/todo.md` 和 `pending-test.md`。本任务按这些规则执行（本 PRD/设计评审通过即视为对计划内文件的改动授权）。

## Notes

- 复杂任务：需要补 `design.md`、`implement.md`，评审后再 `task.py start`。
