// Package dto exercises an imported, exported recursive DTO.
package dto

import (
	"encoding/json"
	"image"
)

type Node struct {
	Name string `json:"name"`
	Next *Node  `json:"next,omitempty"`
}

type Byte uint8
type Raw = json.RawMessage

// A distinct byte type does not acquire RawMessage's methods or JSON semantics.
type RawBytes json.RawMessage

type Value struct {
	Point      image.Point         `json:"point"`
	Count      int                 `json:"count"`
	Node       *Node               `json:"node"`
	Labels     map[string][]string `json:"labels"`
	Pair       [2]int              `json:"pair"`
	Bytes      []byte              `json:"bytes"`
	NamedBytes []Byte              `json:"named_bytes"`
	Payload    Raw                 `json:"payload,omitempty"`
	Optional   *Raw                `json:"optional"`
	Documents  []Raw               `json:"documents"`
	RawBytes   RawBytes            `json:"raw_bytes"`
}
