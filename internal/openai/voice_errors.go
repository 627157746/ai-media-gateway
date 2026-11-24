package openai

import (
	"context"
)

// voiceWrongProvider explains why a 音色库 id did not resolve for the requested
// provider, instead of letting the raw id reach the vendor (where it shows up as
// an opaque "Reference not found"). It returns a 400 either way: a vc_* id is
// ours, so failing to resolve it means the client paired it with the wrong model.
func (h *handler) voiceWrongProvider(ctx context.Context, wantProvider, voiceID string) error {
	v, err := h.st.GetVoice(ctx, voiceID)
	if err != nil || v == nil {
		return bad("音色 %s 不存在（可能已被删除），请重新拉取音色列表", voiceID)
	}
	if !v.Enabled {
		return bad("音色 %q 已停用，请在网关后台重新启用", v.Name)
	}
	if wantProvider != v.ProviderID {
		return bad("音色 %q 属于平台 %s，但当前模型使用的是平台 %s；请换成该平台下的音色，或改用 %s 的模型",
			v.Name, v.ProviderID, wantProvider, v.ProviderID)
	}
	return bad("音色 %q 无法解析到上游音色 ID，请检查网关后台的音色配置", v.Name)
}
