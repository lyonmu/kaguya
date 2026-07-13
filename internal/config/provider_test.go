package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderInfoDefaultModel(t *testing.T) {
	tests := []struct {
		name     string
		provider ProviderInfo
		wantID   string
		wantErr  string
	}{
		{
			name:     "single default",
			provider: ProviderInfo{Models: []ModelInfo{{ID: "model", IsDefault: true}}},
			wantID:   "model",
		},
		{
			name:     "no default",
			provider: ProviderInfo{Models: []ModelInfo{{ID: "model"}}},
			wantErr:  "exactly one default model is required",
		},
		{
			name: "two defaults",
			provider: ProviderInfo{Models: []ModelInfo{
				{ID: "a", IsDefault: true},
				{ID: "b", IsDefault: true},
			}},
			wantErr: "exactly one default model is required",
		},
		{
			name:     "empty provider has no models",
			provider: ProviderInfo{},
			wantErr:  "exactly one default model is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.provider.DefaultModel()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("DefaultModel() error = nil, want %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("DefaultModel() error = %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DefaultModel() unexpected error: %v", err)
			}
			if got.ID != tt.wantID {
				t.Fatalf("DefaultModel() ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	validYAML := `provider_info:
  - name: openai
    base_url: https://api.openai.com
    api_key: sk-test
    protocol: openai
    models:
      - name: GPT-4o
        id: gpt-4o
        is_default: true
`
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid config",
			yaml: validYAML,
		},
		{
			name: "empty provider name",
			yaml: `provider_info:
  - name: ""
    base_url: https://api.openai.com
    protocol: openai
    models:
      - name: GPT-4o
        id: gpt-4o
        is_default: true
`,
			wantErr: "provider name is required",
		},
		{
			name: "invalid protocol",
			yaml: `provider_info:
  - name: openai
    base_url: https://api.openai.com
    protocol: unknown
    models:
      - name: GPT-4o
        id: gpt-4o
        is_default: true
`,
			wantErr: "unsupported protocol",
		},
		{
			name: "empty base url",
			yaml: `provider_info:
  - name: openai
    base_url: ""
    protocol: openai
    models:
      - name: GPT-4o
        id: gpt-4o
        is_default: true
`,
			wantErr: "base_url is required",
		},
		{
			name: "empty model id",
			yaml: `provider_info:
  - name: openai
    base_url: https://api.openai.com
    protocol: openai
    models:
      - name: GPT-4o
        id: ""
        is_default: true
`,
			wantErr: "model id is required",
		},
		{
			name: "no default model",
			yaml: `provider_info:
  - name: openai
    base_url: https://api.openai.com
    protocol: openai
    models:
      - name: GPT-4o
        id: gpt-4o
        is_default: false
`,
			wantErr: "exactly one default model is required",
		},
		{
			name: "two default models",
			yaml: `provider_info:
  - name: openai
    base_url: https://api.openai.com
    protocol: openai
    models:
      - name: A
        id: a
        is_default: true
      - name: B
        id: b
        is_default: true
`,
			wantErr: "exactly one default model is required",
		},
		{
			name:    "file does not exist",
			yaml:    "",
			wantErr: "failed to load config",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if tt.name == "file does not exist" {
				path = filepath.Join(t.TempDir(), "nonexistent.yml")
			} else {
				if err := os.WriteFile(path, []byte(tt.yaml), 0644); err != nil {
					t.Fatalf("failed to write temp config: %v", err)
				}
			}
			cfg, err := Load(path)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() error = nil, want containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if len(cfg.Providers) == 0 {
				t.Fatal("Load() returned config with no providers")
			}
		})
	}
}
