package service

import "testing"

func TestApplyBindingsRejectsMissingTarget(t *testing.T) {
	prompt := map[string]any{
		"303": map[string]any{"inputs": map[string]any{"value": "stale prompt"}},
	}
	params := map[string]any{"motion_prompt": "compiled motion"}
	if err := applyBindings(prompt, []InputBinding{{Node: "999", Input: "value", Param: "motion_prompt"}}, params); err == nil {
		t.Fatal("missing binding target must fail instead of leaving stale workflow input")
	}
}

func TestApplyBindingsAllowsMissingOptionalParam(t *testing.T) {
	prompt := map[string]any{
		"325": map[string]any{"inputs": map[string]any{"image": "stale.png"}},
	}
	if err := applyBindings(prompt, []InputBinding{{Node: "325", Input: "image", Param: "partner_identity_image"}}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
}
