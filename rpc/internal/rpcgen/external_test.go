package rpcgen

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalModule(t *testing.T) {
	module, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	consumerModule := fmt.Sprintf("module example.com/consumer\n\ngo 1.27\nrequire github.com/RussellLuo/gordis/rpc v0.0.0\nreplace github.com/RussellLuo/gordis/rpc => %s\n", filepath.ToSlash(module))
	write("go.mod", consumerModule)
	write("contract/service.go", `package contract
import("context";"encoding/json";"encoding/json/jsontext";"github.com/RussellLuo/gordis")
const Version = "external/1"
type Label string
type Flag = bool
type Raw = json.RawMessage
type Node struct {Name Label; Next *Node}
type Value struct { Enabled Flag; Number float64; Labels map[Label]*Node; Pair [2]int; Values []string; Payload Raw }
type Yield func(Value)error
type Service interface {
 Echo(context.Context, Value)(Value,error)
 EchoRaw(context.Context, Raw)(jsontext.Value,error)
 Stream(context.Context, Flag, int64, float64, Yield)error
}
var Key = gordis.NewKey[Service]("external/service")
`)
	write("business/plugin.go", `package business
import("context";"example.com/consumer/contract";"github.com/RussellLuo/gordis")
type Plugin struct{}
func(*Plugin)Spec()gordis.PluginSpec{return gordis.PluginSpec{ID:"external/business",New:func()gordis.Plugin{return &Plugin{}},Provides:[]gordis.ServiceSpec{contract.Key.Spec()}}}
func(p *Plugin)Activate(_ context.Context,s *gordis.Scope)error{return s.Provide(contract.Key,contract.Service(p))}
func(*Plugin)Echo(_ context.Context,v contract.Value)(contract.Value,error){return v,nil}
func(*Plugin)EchoRaw(_ context.Context,v contract.Raw)(contract.Raw,error){return v,nil}
func(*Plugin)Stream(_ context.Context,b contract.Flag,n int64,f float64,y contract.Yield)error{return y(contract.Value{Enabled:b,Number:float64(n)+f})}
`)
	// Packages.Load and downstream commands must resolve only this temporary
	// module plus explicit mappings for unpublished local libraries.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	out := filepath.Join(dir, "bindings/service.go")
	o := Options{Dir: filepath.Join(dir, "contract"), Type: "Service", Key: "Key", Version: "Version", Package: "bindings", Out: out}
	if err := Generate(o); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(out)
	if err := Generate(o); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(out)
	if !bytes.Equal(first, second) || bytes.Contains(first, []byte(dir)) || bytes.Contains(first, []byte(module)) {
		t.Fatal("unstable or machine-specific generation")
	}
	o.Check = true
	if err := Generate(o); err != nil {
		t.Fatal(err)
	}
	write("bindings/generated_test.go", `package bindings
import("context";"encoding/json";"errors";"reflect";"testing";"example.com/consumer/business";"example.com/consumer/contract";rpc "github.com/RussellLuo/gordis/rpc")
type invoker struct{h rpc.Handler}
func(i invoker)Call(ctx context.Context,m string,in,out any)error{raw,e:=json.Marshal(in);if e!=nil{return e};r,e:=i.h.Methods[m](ctx,raw);if e!=nil{return e};return json.Unmarshal(r,out)}
func(i invoker)Stream(ctx context.Context,m string,in any,y func(json.RawMessage)error)error{r,e:=json.Marshal(in);if e!=nil{return e};return i.h.Streams[m](ctx,r,y)}
func TestExternalCalls(t *testing.T){
 c:=&serviceClient{invoker{newServiceHandler(&business.Plugin{})}}
 want:=contract.Value{Enabled:true,Number:1.5,Labels:map[contract.Label]*contract.Node{"one":{Name:"root",Next:&contract.Node{Name:"leaf"}}},Pair:[2]int{1,2},Values:[]string{"ok"},Payload:json.RawMessage("{\"n\":9007199254740993}")}
 if got,e:=c.Echo(context.Background(),want);e!=nil||!reflect.DeepEqual(got,want){t.Fatalf("%+v %v",got,e)}
 if got,e:=c.EchoRaw(context.Background(),want.Payload);e!=nil||string(got)!=string(want.Payload){t.Fatalf("%s %v",got,e)}
 stop:=errors.New("local stop")
 if e:=c.Stream(context.Background(),true,2,0.5,func(v contract.Value)error{if !v.Enabled||v.Number!=2.5{t.Fatal(v)};return stop});e!=stop{t.Fatal(e)}
}
`)
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("go", args...)
		c.Dir = dir
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("external go %v: %v\n%s", args, err, b)
		}
		return string(b)
	}
	run("test", "-race", "-count=1", "./...")
	for _, pkg := range []string{"contract", "business", "bindings"} {
		deps := run("list", "-deps", "./"+pkg)
		for _, dep := range strings.Fields(deps) {
			if strings.Contains(dep, "github.com/RussellLuo/gordis/rpc/internal/") || strings.Contains(dep, "github.com/RussellLuo/gordis/rpc/examples/") || strings.HasPrefix(dep, "golang.org/x/tools/") {
				t.Fatalf("external dependency leak: %s", dep)
			}
			if pkg != "bindings" && strings.HasPrefix(dep, "github.com/RussellLuo/gordis/rpc") {
				t.Fatalf("ordinary %s imports RPC: %s", pkg, dep)
			}
		}
	}
	// Downstream compilation uses only the committed generated source.
	run("build", "-mod=readonly", "./...")
}
