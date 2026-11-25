# Implement：执行计划

所有路径都相对画布仓库 `infinite-canvas/`。每完成一步在画布仓库本地提交一次，不推送（提交前先列计划给用户确认）。

验证口径：画布 AGENTS.md 写着「写完不跑构建，用户自己做」。经用户同意后，只跑画布 Go 后端的 `go build ./...` 和 `go test ./service/...`（改了 Go 代码，这一步成本低）；前端不跑 `bun run build` / 类型检查，交付时标为「未验证」。

## Step 0 核对（只读）
- `buildApiUrl` 怎么拼接 `/v1`
- `ImageSettingsPanel`、`VideoSettingsPanel`、`AudioSettingsPanel` 的调用方，以及它们的 `onConfigChange` 写法
- `SettingGroup`、`OptionPill` 有没有导出
- 画布节点的首尾帧、参考素材上传入口在哪判断
- 图片请求的入口分支（`requestImages` / `requestImageGenerationSingle` / `requestImageEditSingle`）
- 画布后端 `/api/v1/*` 代理：音色列表有没有，multipart 是不是原样透传
- 后台设置是不是以 JSON 存储

## Step 1 协议与存储
- `web/src/lib/model-channel.ts`：新增 `aimg`
- `service/model_protocol.go`：新增常量并登记
- `model/setting.go`：`ModelChannel`、`PublicModelChannelInfo` 加 `ModelSpecs`
- `service/settings.go`：公共渠道下发时过滤并带上 `ModelSpecs`
- `web/src/stores/use-config-store.ts`：`LocalModelChannel` 和 `publicChannels` 类型加 `modelSpecs`；新增 `imageBackground`、`imageFormat`、`audioLanguage` 三个配置键和默认值
- 新文件 `web/src/lib/ai-media-gateway.ts`：类型定义、`kind` 映射、`gatewaySpecForConfig`

## Step 2 获取模型
- 本地渠道：`fetchGatewayModelSpecs`（`web/src/services/api/image.ts`），并在 `app-config-modal.tsx` 接入
- 后台渠道：Go `fetchGatewayChannelModelSpecs`、handler、`/settings/channel-model-specs` 路由，加一个 httptest 单测；前端 `admin.ts` 的 `fetchChannelModelSpecs` 和后台设置页接入

## Step 3 面板
- 新文件 `web/src/components/gateway-settings.tsx`，包含三个面板和 `useGatewayVoices`
- 三个原面板开头加网关分支
- `video/page.tsx` 和画布节点的素材上传入口加网关分支

## Step 4 请求
- 视频：`createGatewayVideoRequestBody`，并让 `createVideoGenerationTask` 绕开按模型名判断的分支
- 图片：在请求入口加网关分支
- 语音：在 `buildAudioSpeechRequest` 加网关分支
- 远程模式：按 Step 0 核对的结果，必要时补音色列表代理

## Step 5 文档
- 画布 `docs/progress/pending-test.md`：写可测试项
- 画布 `docs/progress/todo.md`：确认需要改的地方
- `CHANGELOG.md` 的 `Unreleased`：加一条概括
- 如有接口或表结构变化，同步更新 `docs/backend/*`

## Step 6 验证
- 画布 Go 后端：`go build ./...`、`go test ./service/...`
- 前端：按画布规则不跑构建，标为「未验证」，交给用户
- 真实联调：用户启动网关和画布后，对照 PRD 验收清单逐项测试
