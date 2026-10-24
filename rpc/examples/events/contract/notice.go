// Package contract defines ordinary event payloads and Topics without RPC.
package contract

import "github.com/RussellLuo/gordis/events"

const PluginID = "example/events/notice-publisher"

type Request struct {
	Message string `json:"message"`
}

type Notice struct {
	Message string `json:"message"`
}

var RequestTopic = events.NewTopic[Request]("example/events/request")
var NoticeTopic = events.NewTopic[Notice]("example/events/notice")
