package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

type configPlugin struct {
	Name string `json:"name"`
}

func (*configPlugin) Spec() Spec {
	return configPluginSpec()
}

func configPluginSpec() Spec {
	return Spec{
		ID:  "config",
		New: func() Plugin { return &configPlugin{Name: "default"} },
	}
}

func TestPrepareConfiguration(t *testing.T) {
	tests := []struct {
		name, raw, want, wantErr string
	}{
		{name: "empty uses constructor default", want: "default"},
		{name: "object", raw: `{"name":"worker"}`, want: "worker"},
		{name: "unknown field", raw: `{"nmae":"worker"}`, wantErr: `unknown field "nmae"`},
		{name: "non-object", raw: `null`, wantErr: "must be a JSON object"},
		{name: "second value", raw: `{} {}`, wantErr: "exactly one JSON object"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := Prepare(configPluginSpec(), json.RawMessage(tt.raw))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Prepare() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := value.(*configPlugin).Name; got != tt.want {
				t.Fatalf("Name = %q, want %q", got, tt.want)
			}
		})
	}
}
