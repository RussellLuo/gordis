// Package contract contains only ordinary Go contracts and Gordis keys.
package contract

import (
	"context"
	"github.com/RussellLuo/gordis"
)

type Text interface {
	Call(context.Context, string) (string, error)
}
type Number interface {
	Value(context.Context) (int, error)
}

var TextKey = gordis.NewKey[Text]("processfixture/text")
var NumberKey = gordis.NewKey[Number]("processfixture/number")
var CallbackKey = gordis.NewKey[Text]("processfixture/callback")
