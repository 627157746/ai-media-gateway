# Provider Adapters

> `internal/provider/<type>/` — one package per vendor, registered via `init()` + `provider.Register`. Shared helpers live in `internal/provider/{httputil,voice}.go`; vendor-neutral types in `internal/media/media.go`.

---

## fal (`internal/provider/fal/fal.go`, type `fal`)

Queue protocol — **always use the URLs fal returns**, never recompute them:

- `POST {base|https://queue.fal.run}/{upstreamModel}` with `Authorization: Key <apiKey>` → `{request_id,status_url,response_url,cancel_url}` → `handle` (the `media.TaskHandle` JSON).
- Poll `status_url`: `IN_QUEUE`→queued, `IN_PROGRESS`→in_progress, `COMPLETED`→fetch `response_url`. **COMPLETED is not success** — failed/cancelled requests report COMPLETED too; a non-2xx result or a non-empty `error` field in the status means failure.
- On ctx cancel/timeout: best-effort `PUT cancel_url` with the `Key` auth header and a detached 5s ctx (`cancelBestEffort`; verified in `TestCancelOnCtxCancel`).
- Polling cadence `pollInterval` (1.5s): transient non-Final `ClassUpstream`/`ClassRateLimit` poll errors retry (RetryAfter honored) up to 5 consecutive; other classes abort; own `Call.Timeout` expiry → Final `ClassUpstream` "fal request <id> timed out after <d>".

### Model `options` schema (`modelOpts`)

```json
{
  "upstream_image": "…", "upstream_reference": "…", "upstream_edit": "…",
  "defaults": {"seed": 42},
  "fields": {"size_mode": "aspect_ratio", "duration_format": "{n}s", "quality": "output_quality", "num_images": ""},
  "drop": ["seed"]
}
```

- **Endpoint selection**: video — (`mode=="reference"`, or reference media (references/videos/audios) with `mode!="frames"`) && `upstream_reference`; else (FirstFrame or LastFrame or References) && `upstream_image`; else `Upstream`. An explicit `frames` mode is never repointed at the reference endpoint. Image edit — `upstream_edit` if set else `Upstream` (`videoEndpoint`, `GenerateImage`).
- **Input build order**: `defaults` → mapped request fields → `Extra` merge → `drop` deletes (`modelOpts.finish`). `fields` maps logical keys to vendor field names; **a field mapped to `""` is omitted** (`modelOpts.field`).
- Image mapping: `prompt`; `N>0`→`num_images`; `size` via `fields.size_mode` (`image_size` default → `{width,height}` from `WxH`, `aspect_ratio` → nearest of the 11-value list, `none`/`auto` → omit); `resolution`; `quality`/`background` only when their `fields` names are configured; edit images → `fields.images` (default `image_urls`), mask → `mask_url`.
- Video mapping: `prompt`; `seconds` → `fields.duration` (default `duration`) formatted by `fields.duration_format` (`string`→`"5"`, `int`→`5`, `{n}s`→`"5s"`); `ratio` else `size` → nearest `aspect_ratio` of 6 values (`auto`/empty → omit); `resolution` lowercased; `generate_audio` only when the pointer is non-nil; `watermark` only if `fields.watermark` configured; `first_frame`→`image_url`, `last_frame`→`end_image_url`. Reference media names depend on the **submitted endpoint**: the reference family (`endpoint == upstream_reference` or path contains `reference-to-video`) gets arrays `reference_image_urls` / `reference_video_urls` / `reference_audio_urls`; any other endpoint gets `image_urls` (array) and scalar `videos[0]`→`video_url`, `audios[0]`→`audio_url`. `options.fields` overrides either name.
- Speech mapping: `input`→`text`, resolved voice→`voice`, `speed>0`→`speed`.
- **Result extraction** walks decoded JSON: images = every `url` in `images[]` else `image.url`; video = `video.url` else first `url` anywhere; audio = `audio.url`/`audio_file.url` else first `url`. No URL → Final `ClassUpstream` "no <kind> url".
- `Speak` downloads the result via `FetchURL` with `c.Stream` (unbounded client) and returns the upstream `Content-Type` (extension fallback via `contentTypeFor`).

## ElevenLabs (`internal/provider/elevenlabs/elevenlabs.go`, type `elevenlabs`, speech only)

- `POST {base|https://api.elevenlabs.io}/v1/text-to-speech/{url-escaped voice}?output_format=X`, `xi-api-key` header, JSON body `{"text","model_id":<Upstream>}`.
- `speed` → `voice_settings.speed` clamped `[0.7,1.2]`; `extra` merges top-level (a `voice_settings` object in `extra` gets speed merged in, not overwritten). `instructions` ignored.
- `output_format`: `""`/`mp3`→`mp3_44100_128`, `opus`→`opus_48000_128`, `pcm`→`pcm_24000`, `wav`→`wav_44100`; anything else → `ClassInvalid`.
- **Voice fallback (`firstVoice`)**: when `ResolveVoice` yields empty, `GET {base}/v1/voices` and take `voices[0].voice_id` — the account's own library, so the adapter works with zero configuration. Cached in a package `sync.Map` keyed `"<base>|<account id>"` for `voiceCacheTTL` (10 min); an **empty library is not cached** (a freshly created voice is picked up on the next call). An empty library → `ClassInvalid` "voice is required …". A non-empty voice — including OpenAI names like `alloy` — is passed through untouched, never remapped (client-visible 404 is intentional).
- Returns `Audio{Body: resp.Body}` — streamed, not buffered.

## Fish Audio (`internal/provider/fishaudio/fishaudio.go`, type `fishaudio`, speech only)

- `POST {base|https://api.fish.audio}/v1/tts`, `Authorization: Bearer <key>`, extra header `model: <Upstream>`.
- Body `{"text","format","reference_id"? (resolved voice, omitted if empty),"prosody":{"speed"} if >0}` + `extra` top-level.
- `format`: `""`/`mp3`,`wav`,`pcm`,`opus`; else `ClassInvalid`.

## Voice resolution (`provider/voice.go` `ResolveVoice`)

`ResolveVoice(call, voice)`: if `voice` non-empty, look it up in the `voices` alias map of **account options → model options → provider options** (first hit wins); if `voice` empty, use `default_voice` from the same chain; else return `voice` unchanged. ElevenLabs then falls back to the account's first library voice (see above); with an empty library too → `ClassInvalid` "voice is required …". Fish Audio simply omits `reference_id`.

### 音色库 takes precedence, and only the adapters may handle the fallback

Before an adapter sees a request, `openai/speech.go` resolves the client-supplied voice against the `voices` table **for the resolved provider only** (`store.FindVoice`, enabled rows, matched on `id` — never on `name`, which is editable and not unique). On a hit it swaps in `upstream_id` and sets `media.SpeechRequest.VoiceResolved = true`.

Every `Speak` implementation must therefore guard its own alias lookup:

```go
voice := r.Voice
if !r.VoiceResolved {
    voice = provider.ResolveVoice(c, voice)
}
```

Without that guard a library `upstream_id` that happens to equal an options alias gets mapped a second time. All three adapters (elevenlabs, fishaudio, fal) carry this guard; a new speech adapter must add it too.

Resolution order is therefore: **音色库 hit → `options.voices` alias → pass-through**. A `vc_*` id that fails to resolve is rejected with a 400 explaining why (`openai/voice_errors.go`), instead of being forwarded upstream as an opaque vendor id. An empty `voices` table leaves behaviour byte-identical to before the feature existed.

## Upstream voice listing (`provider.VoiceLister`)

`ListVoices(ctx, call) ([]UpstreamVoice, error)` powers the admin 从上游导入 picker. Implemented by **elevenlabs** (`GET /v1/voices`, `voice_id` + `name` + `category`/`labels.language` as `detail`; non-empty results cached for `voiceCacheTTL`, empty not cached so new voices appear at once) and **fishaudio** (`GET {base}/model?self=true&page_size=100` — **not** `/v1/model`, which 404s on the live API; keeps only `type=="tts"` with `state=="trained"` because anything else 4xxes at synthesis; `_id` is the `reference_id`). **fal deliberately does not implement it** — its `voice` is a model-specific field with no library behind it — so `admin` answers 400 "不支持自动拉取音色，请手动填写上游音色 ID".

The admin endpoint reuses `handler.upstreamCall` (shared with `upstreamModels`) for account selection, proxy resolution and key decryption, and every upstream error must pass through `scrubKey`.

## Muse video (`internal/provider/muse/video.go`, type `muse`)

- Muse always renders a ~10s mp4 and has no duration/aspect/resolution/audio/watermark controls. `seconds` (non-0), `size`/`ratio` (non-`auto`), `resolution` (non-empty), `generate_audio`/`watermark` (present) are **dropped, not rejected** — Infinite Canvas' OpenAI template sends `seconds=8,size=1280x720,resolution_name=720p,generate_audio=true,watermark=false` by default, and rejecting made every Canvas call fail.
- The adapter reports the dropped names through the optional `provider.VideoParamIgnorer` (`IgnoredVideoParams`); `buildVideoInput` never reads those fields, so the submitted body is identical to one without them (`TestVideoIgnoresUnsupportedControls`). The handler writes the names to `request_logs.params.ignored` — the adapter itself does not log.
- Other checks still fail fast: empty prompt, reference-image limits, etc.

## Rules for adapters

- Get HTTP clients from `Call` (`c.HTTP` for vendor calls, `c.Stream` for unbounded downloads, `c.Timeout` for generation deadlines); base URL via `c.BaseURL(def)` (account > provider > default).
- Classify every non-2xx per `error-handling.md`; use `TransportError`, `ReadErrorBody`, `ParseRetryAfter`, `FetchURL` from `httputil.go`.
- **Post-submit errors are `Final`** — see `error-handling.md` §Final; never let a paid job's failure look retryable.
- Tests: httptest fake vendor in the same package (`fakeQueue`/`captured` fixtures); cover request shape, headers, voice/format mapping, and each error class.
