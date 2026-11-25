# 视频接口字段统一为 infinite-canvas OpenAI 渠道格式

## Goal

网关 `/v1/videos` 的请求字段**只采用** infinite-canvas（tigerowo/infinite-canvas，main）OpenAI 渠道的命名，不再接受网关原有的同义字段名；同时让下载需鉴权的视频结果（Muse）能在画布中播放。目标模型：`h3-max-video`、`wan-3.0-video`、`muse-video`。

## Background（已核实事实）

- 画布 OpenAI 渠道视频请求：`web/src/services/api/video.ts` `createVideoRequestBody`（副本 `.tmp/ic/web_video.ts` 约 470–522 行），multipart `POST {baseUrl}/v1/videos`：
  - `model`、`prompt`、`seconds`、`size`（`宽x高`；seedance 类模型为比例如 `16:9`/`adaptive`）、`resolution_name`、`preset=normal`
  - `input_reference[]`（参考图）、`first_frame_url` / `last_frame_url`（首尾帧）、`video_reference[]` / `audio_reference[]`：每项为 File **或** 公网 http(s) URL 字符串
  - `video_generate_audio`、`video_watermark`（tokendance 渠道发送）
  - 不发送 `mode`、`ratio`、`watermark`、`extra`
- 画布 JSON 形态（火山方舟渠道 `createArkSeedanceVideoRequestBody`，约 525 行）使用同一套名字：`resolution_name`、`video_generate_audio`、`video_watermark`、`input_reference[]`、`first_frame_url`、`last_frame_url`、`video_reference[]`、`audio_reference[]`（值为 URL 字符串数组）。
- 轮询 `GET /v1/videos/{id}` 与画布兼容；画布仅当 `video_url` 路径为 `/videos/{id}/content` 时带 `Authorization` 下载（`cacheProtectedVideo` 约 361 行）。Muse 的 `ResultURL` 需 Key 才能下载（`internal/provider/muse/video.go` `PollVideo`）。
- 网关内仍在用旧字段名的调用方：内置试用页 `internal/web/assets/pages/playground.js` 第 485–498 行（`first_frame`/`last_frame`/`image[]`/`video[]`/`audio[]`/`ratio`/`resolution`/`mode`）；`README.md` 第 201 行。
- 仓库根 `Infinite Canvas.md`（自定义脚本模板）不再作为契约参考，已按用户要求删除；契约源改为画布 OpenAI 渠道源码。

## Requirements

- R1 字段统一（multipart 与 JSON 同名）：

  | 画布字段（唯一接受） | 映射 | 移除的旧名 |
  |---|---|---|
  | `input_reference[]` | References | `image`、`image[]`、`images`、`input_reference`（单个=首帧） |
  | `first_frame_url` | FirstFrame | `first_frame` |
  | `last_frame_url` | LastFrame | `last_frame` |
  | `video_reference[]` | Videos | `video`、`video[]`、`videos` |
  | `audio_reference[]` | Audios | `audio`、`audio[]`、`audios` |
  | `video_generate_audio` | GenerateAudio | `generate_audio` |
  | `video_watermark` | Watermark | `watermark` |
  | `resolution_name` | Resolution | `resolution` |

  - 媒体字段接受上传文件或 data URL / http(s) URL 字符串；http(s) 透传不抓取（SSRF 约定不变）。
- R2 不发 `mode` 时沿用 `normalizeMode`（1–2 张 → `frames`，>2 → `reference`）。
- R3 `provider.VideoFetcher` 适配器（Muse）完成后的 `video_url` 返回网关 `/v1/videos/{id}/content` 绝对地址（`X-Forwarded-Proto/Host` 优先）；`result_url` 仍存厂商地址；fal 不变。（已实现）
- R4 网关自带调用方同步改用新字段名：`playground.js` 视频面板、`README.md`、`.trellis/spec/backend/openai-api.md`；相关测试改为新字段名。

## Acceptance Criteria

- [ ] 画布形状 multipart（文件与 URL 字符串两种）提交后适配器收到全部参考素材与开关。
- [ ] 画布 JSON 形状（URL 字符串数组）同样被接收。
- [ ] 旧字段名（如 `first_frame`、`image[]`、`generate_audio`）不再被识别。
- [ ] 1 张 `input_reference[]` 无 `mode` → `frames` 首帧。
- [ ] Muse 完成任务返回网关 `/content` 地址，fal 返回厂商地址。
- [ ] 试用页视频面板用新字段名可正常提交。
- [ ] `go vet ./...`、`go test ./...`、`go build` 通过。

## Out of Scope

- 语音（MiMo 404、音色限制）——另立任务。
- 修改 infinite-canvas 本身。

## Status（任务按用户要求在此结束）

- 已交付：R2、R3；R1 的画布字段名全部可用（文件 + URL 字符串），但**旧字段名尚未移除**（当前新旧并存）。
- 未做（延后）：R1 中移除旧字段名、`video_watermark` / JSON 形态的画布字段名、R4（试用页、README 改用新字段名）；`mode`、`ratio`/`aspect_ratio`、`extra` 的去留未决定。
