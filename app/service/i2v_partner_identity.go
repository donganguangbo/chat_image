package service

import "fmt"

const (
	i2vPartnerImageNodeID = "325"
	i2vFirstFrameNodeID   = "327"
)

func applyOptionalI2VPartnerIdentity(prompt map[string]any, params map[string]any) error {
	firstFrameNode, ok := prompt[i2vFirstFrameNodeID].(map[string]any)
	if !ok {
		return fmt.Errorf("first-frame node %s is missing", i2vFirstFrameNodeID)
	}
	inputs, ok := firstFrameNode["inputs"].(map[string]any)
	if !ok {
		return fmt.Errorf("first-frame node %s inputs are missing", i2vFirstFrameNodeID)
	}
	if !hasRequiredParam(params, "partner_identity_image") {
		delete(inputs, "image2")
		delete(prompt, i2vPartnerImageNodeID)
		return nil
	}
	if _, ok := prompt[i2vPartnerImageNodeID].(map[string]any); !ok {
		return fmt.Errorf("partner identity image node %s is missing", i2vPartnerImageNodeID)
	}
	inputs["image2"] = []any{i2vPartnerImageNodeID, 0}
	return nil
}
