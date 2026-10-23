package rpcgen

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/tools/go/packages"
)

const gordisPath = "github.com/RussellLuo/gordis"

type method struct {
	name   string
	args   []types.Type
	result types.Type
	yield  types.Type // callback parameter type; only the final synchronous yield is special
	chunk  types.Type
}
type field struct {
	name string
	typ  types.Type
}
type model struct {
	pkg     *packages.Package
	methods []method
	types   []types.Type
	ids     map[types.Type]int
	fields  map[*types.Struct][]field
}

func analyze(dir, outDir string, o Options) (*model, error) {
	pkgs, err := packages.Load(&packages.Config{Dir: dir, Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedTypesSizes | packages.NeedModule}, ".")
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("expected one contract package, got %d", len(pkgs))
	}
	p := pkgs[0]
	var loadErrors []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrors = append(loadErrors, e.Error())
		}
	})
	if len(loadErrors) > 0 {
		sort.Strings(loadErrors)
		return nil, fmt.Errorf("load contract: %s", strings.Join(loadErrors, "\n"))
	}
	if p.Name == "main" || p.Module == nil {
		return nil, fmt.Errorf("contract must be an importable module package")
	}
	rel, err := filepath.Rel(p.Module.Dir, outDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("output must be inside the contract module")
	}
	outPath := p.Module.Path
	if rel != "." {
		outPath += "/" + filepath.ToSlash(rel)
	}
	var cycle bool
	packages.Visit(pkgs, func(p *packages.Package) bool {
		if p.PkgPath == outPath {
			cycle = true
		}
		return true
	}, nil)
	if cycle {
		return nil, fmt.Errorf("contract imports output package %s; generation would create an import cycle", outPath)
	}
	m := &model{pkg: p, ids: map[types.Type]int{}, fields: map[*types.Struct][]field{}}
	obj, ok := p.Types.Scope().Lookup(o.Type).(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("%s: %s: expected interface type", dir, o.Type)
	}
	service := obj.Type()
	if n, ok := types.Unalias(service).(*types.Named); ok && n.TypeParams().Len() != 0 {
		return nil, m.fail(obj.Pos(), o.Type, "generic service interfaces are unsupported")
	}
	iface, ok := service.Underlying().(*types.Interface)
	if !ok || !iface.IsMethodSet() {
		return nil, m.fail(obj.Pos(), o.Type, "expected closed ordinary interface")
	}
	iface.Complete()
	if iface.NumMethods() == 0 {
		return nil, m.fail(obj.Pos(), o.Type, "service must declare at least one method")
	}
	key := p.Types.Scope().Lookup(o.Key)
	if key == nil {
		return nil, m.fail(obj.Pos(), o.Type, "missing Key symbol "+o.Key)
	}
	if _, ok := key.(*types.Var); !ok {
		return nil, m.fail(key.Pos(), o.Type, "Key must be a gordis.Key variable")
	}
	n, ok := types.Unalias(key.Type()).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != gordisPath || n.Obj().Name() != "Key" || n.TypeArgs().Len() != 1 || !types.Identical(n.TypeArgs().At(0), service) {
		return nil, m.fail(key.Pos(), o.Type, "Key must be exactly gordis.Key["+o.Type+"]")
	}
	v, ok := p.Types.Scope().Lookup(o.Version).(*types.Const)
	if !ok || v.Val().Kind() != constant.String || constant.StringVal(v.Val()) == "" {
		return nil, m.fail(obj.Pos(), o.Type, "Version must be a nonempty string constant")
	}
	for i := 0; i < iface.NumMethods(); i++ {
		f := iface.Method(i)
		label := o.Type + "." + f.Name()
		if !f.Exported() {
			return nil, m.fail(f.Pos(), label, "unexported methods are unsupported")
		}
		sig := f.Type().(*types.Signature)
		if sig.Variadic() || sig.TypeParams().Len() != 0 {
			return nil, m.fail(f.Pos(), label, "variadic or generic methods are unsupported")
		}
		params, results := sig.Params(), sig.Results()
		if params.Len() == 0 || !isContext(params.At(0).Type()) {
			return nil, m.fail(f.Pos(), label, "first parameter must be context.Context")
		}
		if results.Len() < 1 || results.Len() > 2 || !types.Identical(results.At(results.Len()-1).Type(), types.Universe.Lookup("error").Type()) {
			return nil, m.fail(f.Pos(), label, "requires one final error and at most one business result")
		}
		method := method{name: f.Name()}
		for j := 1; j < params.Len(); j++ {
			t := params.At(j).Type()
			if yield, ok := t.Underlying().(*types.Signature); ok {
				if j != params.Len()-1 || results.Len() != 1 || yield.Variadic() || yield.Params().Len() != 1 || yield.Results().Len() != 1 || !types.Identical(yield.Results().At(0).Type(), types.Universe.Lookup("error").Type()) {
					return nil, m.fail(f.Pos(), label, "server-stream requires final func(Chunk) error parameter and only error result")
				}
				var typeObj *types.TypeName
				switch t := t.(type) {
				case *types.Named:
					typeObj = t.Obj()
				case *types.Alias:
					typeObj = t.Obj()
				}
				if typeObj != nil && !typeObj.Exported() {
					return nil, m.fail(f.Pos(), label, "yield type must be exported")
				}
				method.yield, method.chunk = t, yield.Params().At(0).Type()
				if err := m.addType(method.chunk); err != nil {
					return nil, m.fail(f.Pos(), label, "stream chunk: "+err.Error())
				}
				continue
			}
			if err := m.addType(t); err != nil {
				return nil, m.fail(f.Pos(), label, fmt.Sprintf("parameter %d: %v", j, err))
			}
			method.args = append(method.args, t)
		}
		if results.Len() == 2 {
			method.result = results.At(0).Type()
			if err := m.addType(method.result); err != nil {
				return nil, m.fail(f.Pos(), label, "result: "+err.Error())
			}
		}
		m.methods = append(m.methods, method)
	}
	sort.Slice(m.methods, func(i, j int) bool { return m.methods[i].name < m.methods[j].name })
	return m, nil
}

func (m *model) fail(pos token.Pos, label, reason string) error {
	return fmt.Errorf("%s: %s: %s", m.pkg.Fset.Position(pos), label, reason)
}
func isContext(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Name() == "Context" && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "context"
}

// Raw JSON has standard, known wire semantics despite implementing Marshaler.
// Go 1.27 aliases encoding/json.RawMessage to jsontext.Value; unalias first so
// both spellings and user aliases work without treating arbitrary []byte as JSON.
func isRawJSON(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	return n.Obj().Pkg().Path() == "encoding/json" && n.Obj().Name() == "RawMessage" ||
		n.Obj().Pkg().Path() == "encoding/json/jsontext" && n.Obj().Name() == "Value"
}

// Each distinct type gets one validator; reserve its ID before descending so
// recursive pointer/slice/map DTOs terminate during analysis and emission.
func (m *model) addType(t types.Type) error {
	if _, ok := m.ids[t]; ok {
		return nil
	}
	m.ids[t] = len(m.types)
	m.types = append(m.types, t)
	if a, ok := t.(*types.Alias); ok {
		if a.Obj().Pkg() != nil && !a.Obj().Exported() {
			return fmt.Errorf("unexported DTO %s", a.Obj().Name())
		}
		return m.addType(types.Unalias(a))
	}
	if isRawJSON(t) {
		return nil
	}
	if pointer, ok := t.(*types.Pointer); ok && isRawJSON(pointer.Elem()) {
		return m.addType(pointer.Elem())
	}
	if n, ok := t.(*types.Named); ok {
		if !n.Obj().Exported() {
			return fmt.Errorf("unexported DTO %s", n.Obj().Name())
		}
		if n.TypeArgs().Len() != 0 {
			return fmt.Errorf("generic DTO %s requires an explicit Codec", n.Obj().Name())
		}
	}
	for _, typ := range []types.Type{t, types.NewPointer(t)} {
		ms := types.NewMethodSet(typ)
		for i := 0; i < ms.Len(); i++ {
			switch ms.At(i).Obj().Name() {
			case "MarshalJSON", "UnmarshalJSON", "MarshalText", "UnmarshalText":
				return fmt.Errorf("custom JSON/text codec on %s requires an explicit Codec", t)
			}
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		if u.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsString) == 0 {
			return fmt.Errorf("unsupported JSON type %s", t)
		}
	case *types.Pointer:
		return m.addType(u.Elem())
	case *types.Slice:
		return m.addType(u.Elem())
	case *types.Array:
		return m.addType(u.Elem())
	case *types.Map:
		key, ok := u.Key().Underlying().(*types.Basic)
		if !ok || key.Kind() != types.String {
			return fmt.Errorf("map keys must be strings: %s", t)
		}
		if err := m.addType(u.Key()); err != nil {
			return err
		}
		return m.addType(u.Elem())
	case *types.Struct:
		seen := map[string]bool{}
		var fields []field
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			tag := strings.Split(reflect.StructTag(u.Tag(i)).Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			if !f.Exported() || f.Anonymous() {
				return fmt.Errorf("field %s: unexported or embedded DTO fields require an explicit Codec", f.Name())
			}
			for _, option := range tag[1:] {
				if option != "" && option != "omitempty" && option != "omitzero" {
					return fmt.Errorf("field %s: unsupported json option %q", f.Name(), option)
				}
			}
			name := tag[0]
			if name == "" {
				name = f.Name()
			}
			// encoding/json silently ignores invalid tag names; reject rather
			// than emitting a validator for a different wire field.
			for _, r := range name {
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" !#$%&()*+-./:;<=>?@[]^_{|}~", r) {
					return fmt.Errorf("field %s: invalid JSON name", f.Name())
				}
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON field %q", name)
			}
			seen[name] = true
			if err := m.addType(f.Type()); err != nil {
				return fmt.Errorf("field %s: %w", f.Name(), err)
			}
			fields = append(fields, field{name, f.Type()})
		}
		m.fields[u] = fields
	default:
		return fmt.Errorf("unsupported JSON type %s; requires an explicit Codec", t)
	}
	return nil
}
