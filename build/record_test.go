package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Record is a public adapter boundary: invalid encoder output must fail at the
// record stage after real compilation/validation and before either delivery.
func TestRecordRejectsInvalidDelivery(t *testing.T) {
	valid := []byte(`{"apiVersion":"example.build/v1"}`)
	for _, tc := range []struct {
		name  string
		files map[string][]byte
	}{
		{"missing", map[string][]byte{"extra.json": valid}},
		{"null", map[string][]byte{"record.json": []byte(`null`)}},
		{"array", map[string][]byte{"record.json": []byte(`[]`)}},
		{"malformed", map[string][]byte{"record.json": []byte(`{"oops":`)}},
		{"escape", map[string][]byte{"record.json": valid, "../escaped.json": valid}},
		{"reserved", map[string][]byte{"record.json": valid, "PROJECT/main.go": valid}},
		{"dependencies", map[string][]byte{"record.json": valid, "dependencies.json": valid}},
		{"case-collision", map[string][]byte{"record.json": valid, "RECORD.JSON": valid}},
		{
			"file-directory",
			map[string][]byte{"record.json": valid, "metadata": valid, "metadata/app.json": valid},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := Request{ModulePath: "example.com/record-check", Dir: t.TempDir(), Output: "program"}
			adapter := valueAdapter()
			adapter.Record = func(facts Facts) (map[string][]byte, error) {
				if facts.BinarySHA256 == "" || len(facts.Commands) == 0 || facts.Commands[len(facts.Commands)-1].ExitCode != 0 {
					t.Fatal("encoder called before successful candidate check", facts)
				}
				return tc.files, nil
			}
			_, err := Build(context.Background(), request, adapter)
			if err == nil || !strings.Contains(err.Error(), "record:") {
				t.Fatal(err)
			}
			absentOutput(t, request)
			if _, err := os.Stat(filepath.Join(request.Dir, "escaped.json")); !os.IsNotExist(err) {
				t.Fatal("encoder escaped record root", err)
			}
		})
	}
}

func TestRecordCancellationRejectsDelivery(t *testing.T) {
	request := localRequest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapter := valueAdapter()
	adapter.Record = func(Facts) (map[string][]byte, error) {
		cancel()
		return map[string][]byte{"record.json": []byte(`{}`)}, nil
	}
	_, err := Build(ctx, request, adapter)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "record:") {
		t.Fatal(err)
	}
	absentOutput(t, request)
}
