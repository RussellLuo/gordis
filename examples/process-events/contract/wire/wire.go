package wire

import (
	"github.com/RussellLuo/gordis/examples/process-events/contract"
	"github.com/RussellLuo/gordis/processbridge"
)

var Notice = processbridge.BindTopic(
	contract.NoticeTopic,
	processbridge.JSONEventCodec[contract.Notice]("example.process-events.notice.json/1"),
	processbridge.EventPublish,
)
