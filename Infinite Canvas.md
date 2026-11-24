请为 Infinite Canvas 编写一段模型调用脚本。能力类型：视频。目标模型：bytedance/seedance-2.5/text-to-video。

请写成一个 async function，把用到的变量写在参数列表里，并把 params 拆成 size、quality、count 等字段，方便对照。不要 import，不要 Markdown 代码围栏。函数内发请求并 return 结果；因为运行时会注入同名局部变量，最后需要 return await 函数名({ 同样的参数 })。

返回要求
脚本内部完成轮询，返回 { url } 或 { blob } 或视频 URL 字符串

可用变量
- prompt (string): 用户输入的提示词（已拼接系统提示词）
- images (string[]): 参考图，dataURL 数组（改图 / 图生视频时有值）
- videos (File[]): 参考视频 File 数组，可直接 form.append；画布或工作台接入的参考视频，无则为空数组
- audios (File[]): 参考音频 File 数组，可直接 form.append；画布或工作台接入的参考音频，无则为空数组
- params (object): 生成参数：生图 {size,resolution,quality,count,background}、视频 {mode,seconds,size,resolution,ratio,generateAudio,watermark}（mode 为 frames 首尾帧或 reference 全能参考；超过 2 张图时为 reference）、音频 {voice,format,speed,instructions}
- model (string): 模型名称（不含渠道前缀）
- baseUrl (string): 渠道接口地址（原样，未拼 /v1）
- apiKey (string): 渠道 API Key，请求头里自己带上
- systemPrompt (string): 系统提示词原文
- http (object): 便捷请求：http.post(path, body, {headers,params,responseType})、http.get(path, opts)、http.url(path)；默认带 Authorization: Bearer apiKey，可用 headers 覆盖；path 相对时按 baseUrl 拼 /v1
- request (function): 原始请求 request({ method, url, headers, params, data, responseType })，不加任何默认头，鉴权头自己写；url 相对时按 baseUrl 拼接（不加 /v1）
- poll (function): 轮询 poll(request, extract, {intervalMs,timeoutMs})，extract 返回真值即结束
- sleep (function): sleep(ms) 延时
- signal (AbortSignal): 取消信号，可透传给 http/request

写法要求
- 写成 async function，参数列表列出用到的变量；params 拆开写出 size、quality、count、seconds 等字段。函数上方用 /** */ 注释写清每个字段。
- 用 request({ method, url, headers, params, data, responseType }) 发原始 HTTP 请求；需要自动带 Authorization: Bearer apiKey 时用 http.post / http.get。
- 相对路径：request 按 baseUrl 拼接且不加 /v1；http 的相对 path 会按 baseUrl 拼 /v1。
- 异步视频任务通常先创建再 poll(request, extract, { intervalMs, timeoutMs })，extract 返回真值即结束。
- images 是参考图 dataURL 字符串数组。videos、audios 是参考视频/音频 File 数组，可直接 form.append(字段名, file)；没有参考时为空数组。
- 使用 FormData 时不要手动设置 Content-Type，交给浏览器带 boundary。
- 函数末尾 return 结果，并在文件最后 return await 函数名({ 同样的参数 })。只输出完整脚本，不要解释。

完整示例（请按实际接口改写，不要原样照搬）

OpenAI 规范
/**
 * OpenAI-compatible video: POST /v1/videos (multipart), then poll GET /v1/videos/{id}.
 * Do not set Content-Type on FormData; the browser adds the boundary.
 * @param {string} prompt
 * @param {string[]} images - reference images as data URLs
 * @param {File[]} videos - reference videos; empty when none
 * @param {File[]} audios - reference audio; empty when none
 * @param {object} params
 * @param {string} params.mode - "frames" uses first/last frame fields; "reference" sends all images as references. More than 2 images become "reference".
 * @param {string|number} params.seconds - duration
 * @param {string} params.size - output size, e.g. "1280x720"
 * @param {string} params.resolution - e.g. "720p"
 * @param {boolean} params.generateAudio
 * @param {boolean} params.watermark
 * @param {string} model
 * @param {string} baseUrl
 * @param {string} apiKey
 * @param {function} request
 * @param {function} poll
 * @returns {Promise<{url: string}|Blob>}
 */
async function generateVideo({
  prompt,
  images,
  videos,
  audios,
  params: {
    mode,
    seconds,
    size,
    resolution,
    generateAudio,
    watermark,
  },
  model,
  baseUrl,
  apiKey,
  request,
  poll,
}) {
  const form = new FormData();
  form.set("model", model);
  form.set("prompt", prompt);
  form.set("seconds", String(seconds || 8));
  form.set("size", String(size || "1280x720"));
  form.set("resolution_name", String(resolution || "720p"));
  form.set("generate_audio", String(generateAudio !== false));
  form.set("watermark", String(watermark === true));
  form.set("mode", mode);
  if (mode === "frames") {
    if (images[0]) {
      form.append("first_frame", await (await fetch(images[0])).blob(), "first.png");
    }
    if (images[1]) {
      form.append("last_frame", await (await fetch(images[1])).blob(), "last.png");
    }
  } else {
    for (const dataUrl of images) {
      form.append("image[]", await (await fetch(dataUrl)).blob(), "ref.png");
    }
  }
  for (const file of videos) {
    form.append("video[]", file);
  }
  for (const file of audios) {
    form.append("audio[]", file);
  }

  const headers = {
    Authorization: `Bearer ${apiKey}`,
  };
  const task = await request({
    method: "post",
    url: `${baseUrl}/v1/videos`,
    headers,
    data: form,
  });

  return await poll(
    async () => {
      const state = await request({
        method: "get",
        url: `${baseUrl}/v1/videos/${task.id}`,
        headers,
      });
      if (state.status === "failed" || state.status === "cancelled") {
        throw new Error(state.error && state.error.message ? state.error.message : "video generation failed");
      }
      if (state.video_url || state.url) {
        return { url: state.video_url || state.url };
      }
      if (state.status === "completed") {
        return await request({
          method: "get",
          url: `${baseUrl}/v1/videos/${task.id}/content`,
          headers,
          responseType: "blob",
        });
      }
      return null;
    },
    (result) => result,
    { intervalMs: 2500, timeoutMs: 300000 },
  );
}

return await generateVideo({
  prompt,
  images,
  videos,
  audios,
  params,
  model,
  baseUrl,
  apiKey,
  request,
  poll,
});

Gemini 规范
/**
 * Gemini Veo video: POST models/{model}:predictLongRunning, then poll the operation.
 * First/last-frame mode: images[0] -> image, images[1] -> lastFrame.
 * Reference mode: all images -> referenceImages.
 * @param {string} prompt
 * @param {string[]} images - reference images as data URLs
 * @param {File[]} videos - reference videos; empty when none
 * @param {File[]} audios - reference audio; empty when none
 * @param {object} params
 * @param {string} params.mode - "frames" or "reference"
 * @param {string|number} params.seconds - sent as durationSeconds
 * @param {string} params.size - pixel size; mapped to aspectRatio when needed
 * @param {string} params.ratio - aspect ratio, e.g. "16:9"
 * @param {string} params.resolution - e.g. "720p"
 * @param {boolean} params.generateAudio
 * @param {boolean} params.watermark - sent as addWatermark
 * @param {string} model
 * @param {string} baseUrl
 * @param {string} apiKey
 * @param {function} request
 * @param {function} poll
 * @returns {Promise<{url: string}>}
 */
async function generateVideo({
  prompt,
  images,
  videos,
  audios,
  params: {
    mode,
    seconds,
    size,
    resolution,
    ratio,
    generateAudio,
    watermark,
  },
  model,
  baseUrl,
  apiKey,
  request,
  poll,
}) {
  async function toInline(source) {
    if (typeof source === "string") {
      const match = source.match(/^data:([^;]+);base64,(.*)$/);
      return {
        bytesBase64Encoded: match ? match[2] : "",
        mimeType: match ? match[1] : "image/png",
      };
    }
    const dataUrl = await new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result || ""));
      reader.onerror = () => reject(reader.error);
      reader.readAsDataURL(source);
    });
    const match = String(dataUrl).match(/^data:([^;]+);base64,(.*)$/);
    return {
      bytesBase64Encoded: match ? match[2] : "",
      mimeType: match ? match[1] : (source.type || "application/octet-stream"),
    };
  }

  const aspectRatioMap = {
    "1280x720": "16:9",
    "1920x1080": "16:9",
    "720x1280": "9:16",
    "1080x1920": "9:16",
  };
  let aspectRatio = ratio || size || "16:9";
  if (aspectRatio === "auto") {
    aspectRatio = "16:9";
  }
  if (aspectRatioMap[aspectRatio]) {
    aspectRatio = aspectRatioMap[aspectRatio];
  }

  const instance = {
    prompt: prompt,
  };
  if (mode === "frames") {
    if (images[0]) {
      instance.image = await toInline(images[0]);
    }
    if (images[1]) {
      instance.lastFrame = await toInline(images[1]);
    }
  } else {
    instance.referenceImages = [];
    for (const dataUrl of images) {
      instance.referenceImages.push({
        image: await toInline(dataUrl),
        referenceType: "asset",
      });
    }
  }
  if (videos[0]) {
    instance.video = await toInline(videos[0]);
  }
  if (audios[0]) {
    instance.audio = await toInline(audios[0]);
  }

  const headers = {
    "Content-Type": "application/json",
    "x-goog-api-key": apiKey,
  };
  const op = await request({
    method: "post",
    url: `${baseUrl}/v1beta/models/${model}:predictLongRunning`,
    headers,
    data: {
      instances: [instance],
      parameters: {
        aspectRatio: aspectRatio,
        durationSeconds: Number(seconds) || 8,
        resolution: resolution || "720p",
        generateAudio: generateAudio !== false,
        addWatermark: watermark === true,
      },
    },
  });

  return await poll(
    () => request({
      method: "get",
      url: `${baseUrl}/v1beta/${op.name}`,
      headers,
    }),
    (state) => {
      if (state.error) {
        throw new Error(state.error.message || "video generation failed");
      }
      if (!state.done) return null;
      const uri = state.response?.generateVideoResponse?.generatedSamples?.[0]?.video?.uri;
      if (!uri) throw new Error("Gemini did not return a video URI");
      if (uri.includes("key=")) return { url: uri };
      const separator = uri.includes("?") ? "&" : "?";
      return { url: uri + separator + "key=" + apiKey };
    },
    { intervalMs: 5000, timeoutMs: 300000 },
  );
}

return await generateVideo({
  prompt,
  images,
  videos,
  audios,
  params,
  model,
  baseUrl,
  apiKey,
  request,
  poll,
});

用户当前草稿（请在此基础上修改；若为空则从零编写）
const modelId = String(model).split("::").pop();
const bodyOf = (r) => (r && r.data !== undefined && r.data !== null ? r.data : r);
const input = { prompt: prompt, aspect_ratio: "9:16", duration: "10s", resolution: "720p" };
if (images && images.length) { input.image_url = images[0]; }
const submit = await request({ method: "post", url: "https://queue.fal.run/" + modelId, headers: { Authorization: "Key " + apiKey, "Content-Type": "application/json" }, data: input });
const submitData = bodyOf(submit);
if (!submitData || !submitData.status_url) throw new Error("fal submit failed: " + JSON.stringify(submitData));
const statusUrl = submitData.status_url; const resultUrl = submitData.response_url;
await poll(async () => { const r = await request({ method: "get", url: statusUrl, headers: { Authorization: "Key " + apiKey } }); const d = bodyOf(r); if (d.status === "ERROR" || d.status === "FAILED") throw new Error("fal generation failed"); return r; }, (r) => { const d = bodyOf(r); return d && d.status === "COMPLETED"; }, { intervalMs: 5000, timeoutMs: 1200000 });
const result = await request({ method: "get", url: resultUrl, headers: { Authorization: "Key " + apiKey } });
const resultData = bodyOf(result);
return { url: resultData.video.url };