// Package processtest provides cleanup assertions for process plugin adapters.
package processtest

import (
	"testing"

	"github.com/RussellLuo/gordis/process"
)

func AssertClosed(t testing.TB, c *process.Client) {
	t.Helper()
	d := c.Snapshot()
	if !d.Reaped || !d.IOComplete || !d.HandlersComplete || d.Pending != 0 || d.Handling != 0 {
		t.Errorf("process not reclaimed: %+v", d)
	}
}
