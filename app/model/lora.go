package model

type VideoLoraWorkflowConfig struct {
	Catalog          string              `yaml:"catalog"`
	AnchorNode       string              `yaml:"anchor_node"`
	BypassFixedLoras map[string][]string `yaml:"bypass_fixed_loras"`
}

type VideoLoraCatalog struct {
	ID       string                      `yaml:"id"`
	Enabled  bool                        `yaml:"enabled"`
	Profiles map[string]VideoLoraProfile `yaml:"profiles"`
	Actions  map[string]string           `yaml:"actions"`
}

type VideoLoraProfile struct {
	PromptPrefix string          `yaml:"prompt_prefix"`
	Loras        []VideoLoraSpec `yaml:"loras"`
}

type VideoLoraSpec struct {
	File     string  `yaml:"file"`
	Strength float64 `yaml:"strength"`
}
