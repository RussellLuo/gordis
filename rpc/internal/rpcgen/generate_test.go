package rpcgen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".rpcgen-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	out, err := filepath.Abs(filepath.Join(dir, "good.go"))
	if err != nil {
		t.Fatal(err)
	}
	return Options{Dir: "testdata/contract", Type: "Good", Key: "Key", Version: "Version", Package: "bindings", Out: out}
}

func TestGenerateDeterminismAndCheck(t *testing.T) {
	o := testOptions(t)
	o.Check = true
	if err := Generate(o); err == nil {
		t.Fatal("missing output passed check")
	}
	if _, err := os.Stat(o.Out); !os.IsNotExist(err) {
		t.Fatal("check wrote output")
	}
	o.Check = false
	if err := Generate(o); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(o.Out)
	if err != nil {
		t.Fatal(err)
	}
	if err := Generate(o); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(o.Out)
	if !bytes.Equal(first, second) {
		t.Fatal("generation is unstable")
	}
	if bytes.Contains(first, []byte(filepath.Dir(o.Out))) {
		t.Fatal("machine paths in generated source")
	}
	o.Check = true
	if err := Generate(o); err != nil {
		t.Fatal(err)
	}
	stale := append(append([]byte(nil), first...), []byte("// stale\n")...)
	if err := os.WriteFile(o.Out, stale, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Generate(o); err == nil {
		t.Fatal("stale output passed check")
	}
	after, _ := os.ReadFile(o.Out)
	if !bytes.Equal(after, stale) {
		t.Fatal("check modified stale output")
	}
	// Exercise the CLI parser without starting another package load.
	var stderr bytes.Buffer
	args := []string{"-type", "Service", "-key", "Key", "-version", "Version", "-package", "bindings", "-out", "unused.go", "unexpected"}
	if err := Run(args, &stderr); err == nil {
		t.Fatal("positional CLI arguments accepted")
	}
}

func TestAnalyzeRejectsAndPreservesOutput(t *testing.T) {
	for _, tc := range []struct{ typ, key, version, want string }{
		{"Good", "WrongKey", "Version", "exactly gordis.Key[Good]"},
		{"Good", "TypeKey", "Version", "Key variable"},
		{"Good", "Key", "EmptyVersion", "nonempty string constant"},
		{"Good", "Key", "MutableVersion", "nonempty string constant"},
		{"BadContext", "BadContextKey", "Version", "first parameter must be context.Context"},
		{"BadReturns", "BadReturnsKey", "Version", "at most one business result"},
		{"BadError", "BadErrorKey", "Version", "final error"},
		{"BadVariadic", "BadVariadicKey", "Version", "variadic"},
		{"BadPrivate", "BadPrivateKey", "Version", "unexported methods"},
		{"BadAny", "BadAnyKey", "Version", "unsupported JSON type"},
		{"BadFunction", "BadFunctionKey", "Version", "server-stream"},
		{"BadChannel", "BadChannelKey", "Version", "unsupported JSON type"},
		{"BadMap", "BadMapKey", "Version", "map keys must be strings"},
		{"BadNested", "BadNestedKey", "Version", "field Values"},
		{"BadCodec", "BadCodecKey", "Version", "explicit Codec"},
		{"BadPrivateRaw", "BadPrivateRawKey", "Version", "unexported DTO"},
		{"Generic", "Key", "Version", "generic service interfaces"},
		{"BadYieldPosition", "BadYieldPositionKey", "Version", "server-stream"},
		{"BadYieldResult", "BadYieldResultKey", "Version", "server-stream"},
		{"BadYieldArity", "BadYieldArityKey", "Version", "server-stream"},
		{"BadYieldVariadic", "BadYieldVariadicKey", "Version", "server-stream"},
		{"BadYieldNoError", "BadYieldNoErrorKey", "Version", "server-stream"},
		{"BadYieldMultiple", "BadYieldMultipleKey", "Version", "server-stream"},
		{"BadYieldAny", "BadYieldAnyKey", "Version", "stream chunk"},
		{"BadYieldNested", "BadYieldNestedKey", "Version", "field Value"},
		{"BadYieldCodec", "BadYieldCodecKey", "Version", "explicit Codec"},
		{"BadYieldPrivate", "BadYieldPrivateKey", "Version", "yield type must be exported"},
		{"BadErrorPayload", "BadErrorPayloadKey", "Version", "unexported DTO error"},
		{"BadPointer", "BadPointerKey", "Version", "unsupported JSON type"},
		{"BadScope", "BadScopeKey", "Version", "explicit Codec"},
		{"BadComplex", "BadComplexKey", "Version", "unsupported JSON type"},
		{"BadPrivateDTO", "BadPrivateDTOKey", "Version", "unexported DTO"},
		{"BadEmbeddedDTO", "BadEmbeddedDTOKey", "Version", "embedded DTO"},
		{"BadPrivateField", "BadPrivateFieldKey", "Version", "unexported"},
		{"BadTag", "BadTagKey", "Version", "unsupported json option"},
		{"BadDuplicateTag", "BadDuplicateTagKey", "Version", "duplicate JSON field"},
		{"BadGenericDTO", "BadGenericDTOKey", "Version", "generic DTO"},
		{"BadChunkFunction", "BadChunkFunctionKey", "Version", "stream chunk"},
		{"BadContextPayload", "BadContextPayloadKey", "Version", "unsupported JSON type"},
		{"BadResult", "BadResultKey", "Version", "result:"},
		{"BadChannelResult", "BadChannelResultKey", "Version", "result:"},
	} {
		t.Run(tc.typ+"/"+tc.key+"/"+tc.version, func(t *testing.T) {
			o := testOptions(t)
			o.Type = tc.typ
			o.Key = tc.key
			o.Version = tc.version
			const original = "original output\n"
			if err := os.WriteFile(o.Out, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			err := Generate(o)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "service.go:") || !strings.Contains(err.Error(), tc.typ) {
				t.Fatalf("diagnostic: %v", err)
			}
			after, _ := os.ReadFile(o.Out)
			if string(after) != original {
				t.Fatal("failed analysis changed original output")
			}
		})
	}
}

func TestLoadFailurePreservesOutput(t *testing.T) {
	o := testOptions(t)
	o.Dir = filepath.Join(filepath.Dir(o.Out), "broken")
	if err := os.Mkdir(o.Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.Dir, "service.go"), []byte("package broken\ntype Good interface {"), 0644); err != nil {
		t.Fatal(err)
	}
	const original = "previous generated file\n"
	if err := os.WriteFile(o.Out, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Generate(o); err == nil || !strings.Contains(err.Error(), "load contract") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(o.Out)
	if string(after) != original {
		t.Fatal("load failure modified output")
	}
}

func TestRejectContractOutputAndCycle(t *testing.T) {
	o := testOptions(t)
	o.Out = "overwrite.go"
	if err := Generate(o); err == nil || !strings.Contains(err.Error(), "separate companion") {
		t.Fatal(err)
	}
	o = testOptions(t)
	// The genfixture contract already imports the output dto package.
	o.Dir = "../genfixture/contract"
	o.Type = "Service"
	o.Out = "../dto/service.go"
	if err := Generate(o); err == nil || !strings.Contains(err.Error(), "import cycle") {
		t.Fatal(err)
	}
}
