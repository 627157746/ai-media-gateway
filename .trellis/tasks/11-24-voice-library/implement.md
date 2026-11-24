# 实施计划：语音音色库（上游导入版）

## 前置

- 任务目录 `.trellis/tasks/11-24-voice-library`，gateway 分支 `master`（无 remote）。
- canvas 克隆在 `D:/project/infinite-canvas`（与 gateway 同级），commit `9cd3dea`。
- 顺序：**先后台闭环，再改 canvas**。两个仓库分开验证，避免跨仓库排障。
- 全程不碰 `Infinite Canvas.md`；canvas 是外部仓库，不改上游远端。

## 阶段 A：后台（gateway）

### S1 迁移 + store

- [ ] 新建 `internal/store/migrations/007_voices.sql`：`voices` 表 + `idx_voices_provider` + `idx_voices_upstream`（UNIQUE on `provider_id, upstream_id`）。
- [ ] 新建 `internal/store/voices.go`：`Voice` 结构体 + `GetVoice` / `ListVoices(providerID)` / `CreateVoice` / `UpdateVoice` / `DeleteVoice` / `FindVoice(providerID, id)`。
- [ ] `options` 列按 `database-guidelines.md` 扫描（先 `[]byte` 再赋 `json.RawMessage`）。
- [ ] 唯一约束冲突 → `store.ErrConflict`。
- [ ] 改 `internal/store/providers.go` 的 `DeleteProvider`：子表计数加 `voices`。
- [ ] **验证**：`go test ./internal/store/`

> Fail-Fast：先跑通迁移最容易翻车的部分（唯一索引 + FK），别等四层写完才发现。

### S2 上游音色拉取

- [ ] `internal/provider/provider.go` 加 `UpstreamVoice` + `VoiceLister` 接口。
- [ ] `internal/provider/elevenlabs/elevenlabs.go` 加 `ListVoices`：读 `voice_id`/`name`/`category`，新开 `voiceListCache`（TTL 复用 `voiceCacheTTL`，空列表不缓存）。
- [ ] `internal/provider/fishaudio/fishaudio.go` 加 `ListVoices`：`GET /model?self=true&page_size=100`，过滤 `type=="tts"` && `state=="trained"`，`Detail` 放 `visibility`。
- [ ] `fal` 不实现。
- [ ] **验证**：`go test ./internal/provider/...`（用 httptest 假上游，不碰真实网络）。

### S3 admin 层

- [ ] 新建 `internal/admin/voices.go`：四条 CRUD + `upstreamVoices`。
- [ ] `upstreamVoices` 照抄 `upstreamModels`（`admin.go:260`）的账号挑选 / 代理解析 / `scrubKey`，只换适配器断言与调用。
- [ ] `admin.go` 注册 5 条路由。
- [ ] 校验按 `design.md` §5.2，非法输入全 400；平台不存在也要显式检查。
- [ ] **验证**：`go build ./cmd/gateway`，起服务 curl 各路由含异常输入。

### S4 `/v1/audio/voices`

- [ ] 新建 `internal/openai/voices.go`，注册 `GET /v1/audio/voices`。
- [ ] 只返回 `enabled` 音色，字段 `{id,name,provider_id}`，**不含 `upstream_id`**，按 `name` 排序。
- [ ] **验证**：`curl -H "Authorization: Bearer <key>" localhost:8090/v1/audio/voices`

### S5 解析与二次映射

- [ ] `internal/media/media.go`：`SpeechRequest` 加 `VoiceResolved bool`。
- [ ] `internal/openai/speech.go`：`Resolve` 之后查库替换，置 `VoiceResolved`。
- [ ] 三个适配器各加一行守卫：`if !r.VoiceResolved { voice = provider.ResolveVoice(c, voice) }`。
- [ ] **验证**：`go test ./internal/openai/ ./internal/provider/...`

### S6 测试

- [ ] `store_test.go`：007 建表、CRUD、(provider_id,upstream_id) 冲突、删平台 409。
- [ ] `admin_test.go`：CRUD 各状态码、导入端点（假 `VoiceLister`）、fal 不支持 400。
- [ ] `openai_test.go`：`/v1/audio/voices` 只回 enabled；解析优先级三档；`VoiceResolved` 防二次映射；库为空回归。
- [ ] **验证**：`go test ./... && go vet ./...`

### S7 管理页

- [ ] 新建 `internal/web/assets/pages/voices.js`：列表 + 表单 + 「从上游导入」勾选面板。
- [ ] 改 `internal/web/assets/app.js`：import + NAV + ROUTES 三处。
- [ ] **验证**：`go build -o bin/gateway.exe ./cmd/gateway`，浏览器走完增删改查与导入。

### S8 后台端到端

- [ ] 建平台 → 建账号 → 建 speech 模型 → 从上游导入音色 → `curl /v1/audio/voices` → `curl /v1/audio/speech` 带音色 `id` → 确认上游收到的是 `upstream_id`。
- [ ] **Fish Audio 真实 key 实测**：这一步不做完，Fish Audio 的导入功能只能标注「未实测」。

## 阶段 B：canvas 前端

### S9 动态音色

- [ ] `web/src/lib/audio-generation.ts`：`normalizeAudioVoiceValue` 放开白名单。
- [ ] `web/src/services/api/audio.ts`：加 `fetchAudioVoices`，path 用 `/audio/voices`（`buildApiUrl` 自动补 `/v1`）。
- [ ] 新增共享 hook `useAudioVoiceOptions`，拉取失败静默回落内置列表。
- [ ] `components/audio-settings-panel.tsx`、`components/layout/app-config-modal.tsx` 改用动态选项。
- [ ] `components/canvas/canvas-audio-settings-popover.tsx` 的标签解析要能显示任意值的 name。
- [ ] **验证**：`pnpm typecheck` + `pnpm dev` 浏览器实测。

## 验证命令

```bash
# gateway
cd /d/project/ai-media-gateway
go build ./cmd/gateway && go vet ./... && go test ./...

# canvas
cd /d/project/infinite-canvas/web
pnpm typecheck && pnpm dev   # 依赖需先 pnpm install
```

## 回滚点

| 阶段 | 回滚 |
|---|---|
| S1 后 | 删 `007_voices.sql`（若 schema_version 已到 7 需手工回退） |
| S2 后 | 删两个 `ListVoices` 方法与接口 |
| S3/S4 后 | 删 `admin/voices.go`、`openai/voices.go`、摘路由 |
| S5 后 | 回退 `speech.go` 查库 10 行 + 三处守卫 + `VoiceResolved` 字段 |
| S9 后 | 回退 canvas 四个文件（仓库可 `git diff` 精确定位） |

## 审查关卡

- S1 完成 → 在**已有数据的旧 DB 副本**上验证升级，不只测全新 DB。
- S2 完成 → 确认空音色列表不缓存（与 `firstVoice` 行为一致）。
- S3 完成 → 所有非法输入是 400 不是 500。
- S5 完成 → `VoiceResolved` 守卫三个适配器都加了，漏一个就会重现二次映射。
- S8 完成 → Fish Audio 实测结论必须如实标注，不得用「文档说可以」代替实测。
- S9 完成 → 断网/错 key 场景下 canvas 不白屏。
