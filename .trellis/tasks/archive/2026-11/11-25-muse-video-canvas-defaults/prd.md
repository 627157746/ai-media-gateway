# muse 视频接受 Canvas OpenAI 模板默认参数

## Goal

Infinite Canvas 用 OpenAI 视频模板（`POST /v1/videos`）调用 muse 渠道的视频模型时能正常出视频，不会因为模板自动填的默认参数被网关拒绝。被忽略的参数要能在请求日志里查到，不悄悄丢掉。

## Background（已核实事实）

- Canvas OpenAI 视频模板（`Infinite Canvas.md` 第 77–84 行）每次都会发这些字段，用户没填时用默认值：
  - `seconds = seconds || 8`
  - `size = size || "1280x720"`
  - `resolution_name = resolution || "720p"`
  - `generate_audio = String(generateAudio !== false)`：默认是 `"true"`
  - `watermark = String(watermark === true)`：默认是 `"false"`
- 网关 `internal/openai/videos.go` 能读到上面所有字段（`resolution`、`resolution_name` 都认，`"true"/"false"` 会转成布尔值）。
- muse 适配器 `rejectVideoControls`（`internal/provider/muse/video.go:163-202`，在 `SubmitVideo` 第 57 行调用）现在的行为：`seconds` 不为 0、`ratio`/`size` 不是 `auto`、`resolution` 非空、`generate_audio`/`watermark` 只要出现，都会返回 `ClassInvalid` + `Final` 错误。所以按代码推断，Canvas 用默认参数调 muse 视频每次都会失败。（未实际发请求验证）
- 请求日志：`videos.go:263` 用 `logParams(r, videoParams(vreq))` 把解析后的参数写进 `request_logs.params`（JSON），后台「请求日志 → 详情」会展示其中所有键（`internal/web/assets/pages/logs.js:90-112`）。
- 约束（`.trellis/spec/backend/logging-guidelines.md:26`）：provider 适配器不写日志，只返回结果或错误；`provider` 包不引用 `httpx`。
- 现有测试 `TestVideoRejectsUnsupportedControls`（`internal/provider/muse/video_test.go:156-191`）断言 seconds/ratio/size/resolution 会被拒、`auto` 放行；没有覆盖 generate_audio/watermark。

## Requirements

- R1：muse 视频遇到 `seconds`、`size`、`ratio`、`resolution`、`generate_audio`、`watermark` 时不再报错，忽略这些字段继续提交，提交给 muse 的请求内容和不带这些字段时完全一样。
- R2：被忽略的字段要记到这次请求的 `request_logs.params` 里，键名是 `ignored`，值是被忽略的字段名列表（例如 `["seconds","size","resolution","generate_audio"]`），后台请求日志详情能看到。
- R3：忽略判断沿用现在的"空值"规则：`seconds` 为空或 0、`size`/`ratio` 为空或 `auto`、`resolution` 为空、`generate_audio`/`watermark` 没出现，都不算被忽略，不写进 `ignored`。
- R4：其他平台（fal）的行为和日志保持不变。
- R5：muse 视频的其他校验照旧，比如 prompt 为空、参考图不合规等，该报错还报错。

## Acceptance Criteria

- [ ] 用 Canvas 默认参数（`seconds=8`、`size=1280x720`、`resolution_name=720p`、`generate_audio=true`、`watermark=false`）调 muse 视频模型，返回 200 并创建任务，不再报 400。
- [ ] 上面这次请求的 `request_logs.params.ignored` 包含 `seconds`、`size`、`resolution`、`generate_audio`、`watermark`。
- [ ] 不带这些字段（或值为 `auto`/0）的 muse 请求，`params` 里没有 `ignored` 键。
- [ ] fal 视频请求的 `params` 里没有 `ignored` 键，行为和现在一样。
- [ ] `TestVideoRejectsUnsupportedControls` 改为断言"会被忽略且照常提交"，并补上 generate_audio/watermark 的用例；`go test ./...`、`go vet ./...` 通过。
- [ ] 相关 spec（`provider-adapters.md` / `logging-guidelines.md` / `openai-api.md` 中涉及 muse 视频或 `params` 的部分）同步更新。

## Technical Notes（初步方案，实施前可调整）

- 在 `internal/provider` 加一个可选接口（类似现有的 `VideoFetcher`），muse 实现它，用来返回"这次请求里会被忽略的字段名"；`rejectVideoControls` 改为只计算这个列表、不再报错。
- `videos.go` 在确定平台之后调用这个接口，有结果时把 `ignored` 加进 `videoParams` 再写日志。这样适配器本身仍然不写日志，也不需要引用 `httpx`。

## Out of Scope

- fal 视频适配器的参数处理。
- muse **图片**的同类拒收（size/quality/background/resolution）：Canvas 没给生图模板，不知道实际会发哪些字段，暂不改。
- 在接口返回里加提示字段（用户选了方案 1：只记日志）。
- 修改 Canvas 模板（不在本仓库）。
