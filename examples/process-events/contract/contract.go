package contract

import "github.com/RussellLuo/gordis/events"

const (
	Package = "example-process-events"
	Version = "1.0.0"
)

type Notice struct {
	Message string `json:"message"`
}

var NoticeTopic = events.NewTopic[Notice]("example.process-events.notice/1")
