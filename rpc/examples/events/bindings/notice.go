// Package bindings keeps event codecs and directions in the assembly layer.
package bindings

import (
	rpcevents "github.com/RussellLuo/gordis/rpc/events"
	"github.com/RussellLuo/gordis/rpc/examples/events/contract"
)

func NewBridge() (*rpcevents.Bridge, error) {
	return rpcevents.NewBridge(
		rpcevents.BindTopic(contract.RequestTopic,
			rpcevents.JSONEventCodec[contract.Request]("example/events/request/json/1"), rpcevents.Subscribe),
		rpcevents.BindTopic(contract.NoticeTopic,
			rpcevents.JSONEventCodec[contract.Notice]("example/events/notice/json/1"), rpcevents.Publish),
	)
}
