package types

import "encoding/json"

type CommonResponse struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    *json.RawMessage `json:"data,omitempty"`
}

type ServerStatus struct {
	ShuttingDown bool   `json:"shutting_down"`
	Restarting   bool   `json:"restarting"`
	PID          int    `json:"pid"`
	Version      string `json:"version"`
	DataPath     string `json:"data_path"`
}

type HealthInfo struct {
	DataPath string `json:"data_path"`
	PID      int    `json:"pid"`
	Version  string `json:"version"`
}

type HealthResponse struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    HealthInfo `json:"data"`
}

type Exclude struct {
	UseGitIgnore bool     `yaml:"use_git_ignore,omitempty" json:"use_git_ignore,omitempty"`
	Customized   []string `yaml:"customized,omitempty"     json:"customized,omitempty"` // Won't be used if enable_git_ignore is true
}

type Filters struct {
	Exclude Exclude  `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Include []string `yaml:"include,omitempty" optional:"true" json:"include,omitempty"`
}
