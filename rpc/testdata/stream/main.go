package main

import (
	"context"
	"fmt"
	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture"
	"github.com/RussellLuo/gordis/rpc/internal/streamfixture/business"
	"os"
)

func main() {
	importOnly := len(os.Args) > 1 && os.Args[1] == "import-only"
	b := streamfixture.Boundary(importOnly)
	u := rpc.Unit{Plugins: []gordis.Plugin{&business.Plugin{ImportOnly: importOnly}}, Instances: []gordis.InstanceSpec{{ID: "business", Plugin: "streamfixture/business"}}, Imports: b.Imports}
	for _, binding := range b.Exports {
		u.Exports = append(u.Exports, rpc.Export{Instance: "business", Binding: binding})
	}
	if err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{Identity: streamfixture.Identity(), Unit: u}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
