package service

import "testing"

func TestI2VWorkflowDisconnectsMissingPartnerIdentity(t *testing.T) {
	prompt := map[string]any{
		"325": map[string]any{"inputs": map[string]any{"image": "stale.png"}},
		"327": map[string]any{"inputs": map[string]any{"image1": []any{"326", 0}, "image2": []any{"325", 0}}},
	}
	if err := applyOptionalI2VPartnerIdentity(prompt, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	inputs := prompt["327"].(map[string]any)["inputs"].(map[string]any)
	if _, ok := inputs["image2"]; ok {
		t.Fatal("image2 must be disconnected without a partner identity image")
	}
	if _, ok := prompt["325"]; ok {
		t.Fatal("unused partner identity image node must be removed")
	}
}

func TestI2VWorkflowConnectsProvidedPartnerIdentity(t *testing.T) {
	prompt := map[string]any{
		"325": map[string]any{"inputs": map[string]any{"image": "partner.png"}},
		"327": map[string]any{"inputs": map[string]any{"image1": []any{"326", 0}}},
	}
	params := map[string]any{"partner_identity_image": "partner.png"}
	if err := applyOptionalI2VPartnerIdentity(prompt, params); err != nil {
		t.Fatal(err)
	}
	inputs := prompt["327"].(map[string]any)["inputs"].(map[string]any)
	link, ok := inputs["image2"].([]any)
	if !ok || len(link) != 2 || link[0] != "325" {
		t.Fatalf("image2 link = %#v", inputs["image2"])
	}
}
