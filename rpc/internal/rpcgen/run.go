// Package rpcgen implements the build-time interface analyzer and generator.
// It is not a dependency of the RPC runtime or generated companion packages.
package rpcgen

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"io"
	"os"
	"path/filepath"
)

type Options struct {
	Dir, Type, Key, Version, Package, Out string
	Check                                 bool
}

// Run loads the contract from the working directory. Flags deliberately name
// Go symbols, rather than repeating the key or version's wire value.
func Run(args []string, stderr io.Writer) error {
	var o Options
	f := flag.NewFlagSet("rpcgen", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&o.Type, "type", "", "exported service interface in the current package")
	f.StringVar(&o.Key, "key", "", "exported gordis.Key[Service] symbol")
	f.StringVar(&o.Version, "version", "", "exported nonempty string constant")
	f.StringVar(&o.Package, "package", "", "generated companion package name")
	f.StringVar(&o.Out, "out", "", "generated Go file, outside the contract package")
	f.BoolVar(&o.Check, "check", false, "check output without writing files")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return Generate(o)
}

func Generate(o Options) error {
	for _, name := range []string{o.Type, o.Key, o.Version} {
		if !token.IsIdentifier(name) || !token.IsExported(name) {
			return fmt.Errorf("type, key and version must name exported Go symbols (got %q)", name)
		}
	}
	if !token.IsIdentifier(o.Package) || o.Package == "_" || o.Package == "main" || o.Out == "" || filepath.Ext(o.Out) != ".go" {
		return errors.New("-package and a .go -out path are required")
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(o.Out) {
		o.Out = filepath.Join(dir, o.Out)
	}
	o.Out = filepath.Clean(o.Out)
	// Resolve existing ancestors so a symlink cannot put output into contract.
	outDir, err := realPath(filepath.Dir(o.Out))
	if err != nil {
		return err
	}
	contractDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if outDir == contractDir {
		return errors.New("output must be in a separate companion package")
	}
	o.Out = filepath.Join(outDir, filepath.Base(o.Out))
	m, err := analyze(contractDir, outDir, o)
	if err != nil {
		return err
	}
	data, err := m.render(o)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(o.Out)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Equal(old, data) {
		return nil
	}
	if o.Check {
		return fmt.Errorf("%s is missing or stale; run go generate", o.Out)
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(outDir, ".rpcgen-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Chmod(0644); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), o.Out)
}

func realPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = realPath(parent)
	return filepath.Join(resolved, filepath.Base(path)), err
}
