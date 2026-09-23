package gordis_test

import (
	"context"
	"errors"
	"testing"

	"github.com/RussellLuo/gordis"
)

func TestKeyName(t *testing.T) {
	key := gordis.NewKey[string]("test.named")
	if got := key.Name(); got != "test.named" {
		t.Fatalf("Name() = %q, want %q", got, "test.named")
	}
	if got := key.Spec().Name(); got != key.Name() {
		t.Fatalf("Spec().Name() = %q, Key.Name() = %q", got, key.Name())
	}
}

func TestScopeContracts(t *testing.T) {
	var old *gordis.Scope
	p := definition("provider", func(_ context.Context, s *gordis.Scope) error {
		old = s
		if _, err := gordis.Get(s, valueKey); err == nil {
			return errors.New("undeclared Get succeeded")
		}
		if err := gordis.Provide(s, valueKey, (*int)(nil)); err == nil {
			return errors.New("nil Provide succeeded")
		}
		v := 1
		must(t, gordis.Provide(s, valueKey, &v))
		if err := gordis.Provide(s, valueKey, &v); err == nil {
			return errors.New("duplicate Provide succeeded")
		}
		return nil
	})
	p.Provides = []gordis.ServiceSpec{valueKey.Spec()}
	h := host(t, p)
	must(t, h.Start(deadline(t)))
	must(t, h.Stop(deadline(t)))
	if err := old.Defer("late", func(context.Context) error { return nil }); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	if err := old.Go("late", func(context.Context) error { return nil }); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := old.Acquire(); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := gordis.Get(old, valueKey); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal(err)
	}
}

func TestServiceDeclarationFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		start    func(context.Context, *gordis.Scope) error
		provides []gordis.ServiceSpec
	}{
		{
			"missing provided service",
			func(context.Context, *gordis.Scope) error { return nil },
			[]gordis.ServiceSpec{valueKey.Spec()},
		},
		{
			"undeclared provide",
			func(_ context.Context, s *gordis.Scope) error {
				v := 1
				return gordis.Provide(s, valueKey, &v)
			},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin := definition("plugin", tc.start)
			plugin.Provides = tc.provides
			h := host(t, plugin)
			if err := h.Start(deadline(t)); err == nil {
				t.Fatal("expected failure")
			}
			s := find(h, "plugin")
			if s.State != gordis.Failed || !s.CleanupComplete || s.Generation != 1 {
				t.Fatal(s)
			}
		})
	}
}
