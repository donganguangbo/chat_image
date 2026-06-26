package model

type ServiceConfig struct {
	ExposeWorkflowCatalog bool                   `yaml:"expose_workflow_catalog"`
	ExposeRawTaskCreate   bool                   `yaml:"expose_raw_task_create"`
	StorageMode           string                 `yaml:"storage_mode"`
	DefaultWorkflows      CategoryWorkflowConfig `yaml:"default_workflows"`
	S3                    S3FileConfig           `yaml:"s3"`
	Text                  TextConfig             `yaml:"text"`
}

type S3FileConfig struct {
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

type TextConfig struct {
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
	APIKey  string `yaml:"api_key"`
}
