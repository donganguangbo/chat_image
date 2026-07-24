package model

type WorkflowConfig struct {
	ID             string                  `yaml:"id"`
	Name           string                  `yaml:"name"`
	Template       string                  `yaml:"template"`
	TemplateFormat string                  `yaml:"template_format"`
	Mode           string                  `yaml:"mode"`
	OutputKind     string                  `yaml:"output_kind"`
	Defaults       map[string]any          `yaml:"defaults"`
	Required       []string                `yaml:"required"`
	Bindings       []InputBinding          `yaml:"bindings"`
	TimeoutSec     int                     `yaml:"timeout_sec"`
	DynamicLora    VideoLoraWorkflowConfig `yaml:"dynamic_lora"`

	ConfigFile   string `yaml:"-"`
	TemplatePath string `yaml:"-"`
}

type InputBinding struct {
	Node  string `yaml:"node"`
	Input string `yaml:"input"`
	Param string `yaml:"param"`
}

type CategoryWorkflowConfig struct {
	TextToImage  string `yaml:"text_to_image"`
	ImageEdit    string `yaml:"image_edit"`
	TextToVideo  string `yaml:"text_to_video"`
	ImageToVideo string `yaml:"image_to_video"`
}
