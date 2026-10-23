package contract

import (
	"context"
	"encoding/json"
	"github.com/RussellLuo/gordis"
	"unsafe"
)

const Version = "1"
const EmptyVersion = ""

type privateRaw = json.RawMessage
type BadPrivateRaw interface {
	Call(context.Context, privateRaw) error
}

var BadPrivateRawKey = gordis.NewKey[BadPrivateRaw]("test/private-raw")

var MutableVersion = "1"

type Good interface {
	Call(context.Context, string) (string, error)
}

var Key = gordis.NewKey[Good]("test/good")
var WrongKey = gordis.NewKey[string]("test/wrong")

type TypeKey = gordis.Key[Good]

type BadContext interface{ Call(string) error }
type BadReturns interface {
	Call(context.Context) (string, int, error)
}
type BadError interface {
	Call(context.Context) (error, string)
}
type BadVariadic interface {
	Call(context.Context, ...string) error
}
type BadPrivate interface{ call(context.Context) error }
type BadAny interface {
	Call(context.Context, any) error
}
type BadFunction interface {
	Call(context.Context, func(int) bool) error
}
type BadChannel interface {
	Call(context.Context, chan int) error
}
type BadMap interface {
	Call(context.Context, map[int]string) error
}
type BadNested interface {
	Call(context.Context, struct{ Values []any }) error
}
type Marshaler string

func (Marshaler) MarshalJSON() ([]byte, error) { return nil, nil }

type BadCodec interface {
	Call(context.Context, Marshaler) error
}
type Generic[T any] interface {
	Call(context.Context, T) error
}

var BadContextKey = gordis.NewKey[BadContext]("test/context")
var BadReturnsKey = gordis.NewKey[BadReturns]("test/returns")
var BadErrorKey = gordis.NewKey[BadError]("test/error")
var BadVariadicKey = gordis.NewKey[BadVariadic]("test/variadic")
var BadPrivateKey = gordis.NewKey[BadPrivate]("test/private")
var BadAnyKey = gordis.NewKey[BadAny]("test/any")
var BadFunctionKey = gordis.NewKey[BadFunction]("test/function")
var BadChannelKey = gordis.NewKey[BadChannel]("test/channel")
var BadMapKey = gordis.NewKey[BadMap]("test/map")
var BadNestedKey = gordis.NewKey[BadNested]("test/nested")
var BadCodecKey = gordis.NewKey[BadCodec]("test/codec")

type privateYield func(int) error
type privateDTO struct{ Value int }
type GenericDTO[T any] struct{ Value T }

type BadYieldPosition interface {
	Call(context.Context, func(int) error, int) error
}

var BadYieldPositionKey = gordis.NewKey[BadYieldPosition]("test/BadYieldPosition")

type BadYieldResult interface {
	Call(context.Context, func(int) error) (int, error)
}

var BadYieldResultKey = gordis.NewKey[BadYieldResult]("test/BadYieldResult")

type BadYieldArity interface {
	Call(context.Context, func(int, int) error) error
}

var BadYieldArityKey = gordis.NewKey[BadYieldArity]("test/BadYieldArity")

type BadYieldVariadic interface {
	Call(context.Context, func(...int) error) error
}

var BadYieldVariadicKey = gordis.NewKey[BadYieldVariadic]("test/BadYieldVariadic")

type BadYieldNoError interface {
	Call(context.Context, func(int)) error
}

var BadYieldNoErrorKey = gordis.NewKey[BadYieldNoError]("test/BadYieldNoError")

type BadYieldMultiple interface {
	Call(context.Context, func(int) error, func(int) error) error
}

var BadYieldMultipleKey = gordis.NewKey[BadYieldMultiple]("test/BadYieldMultiple")

type BadYieldAny interface {
	Call(context.Context, func(any) error) error
}

var BadYieldAnyKey = gordis.NewKey[BadYieldAny]("test/BadYieldAny")

type BadYieldNested interface {
	Call(context.Context, func(struct{ Value chan int }) error) error
}

var BadYieldNestedKey = gordis.NewKey[BadYieldNested]("test/BadYieldNested")

type BadYieldCodec interface {
	Call(context.Context, func(Marshaler) error) error
}

var BadYieldCodecKey = gordis.NewKey[BadYieldCodec]("test/BadYieldCodec")

type BadYieldPrivate interface {
	Call(context.Context, privateYield) error
}

var BadYieldPrivateKey = gordis.NewKey[BadYieldPrivate]("test/BadYieldPrivate")

type BadErrorPayload interface {
	Call(context.Context, error) error
}

var BadErrorPayloadKey = gordis.NewKey[BadErrorPayload]("test/BadErrorPayload")

type BadPointer interface {
	Call(context.Context, unsafe.Pointer) error
}

var BadPointerKey = gordis.NewKey[BadPointer]("test/BadPointer")

type BadScope interface {
	Call(context.Context, *gordis.Scope) error
}

var BadScopeKey = gordis.NewKey[BadScope]("test/BadScope")

type BadComplex interface {
	Call(context.Context, complex128) error
}

var BadComplexKey = gordis.NewKey[BadComplex]("test/BadComplex")

type BadPrivateDTO interface {
	Call(context.Context, privateDTO) error
}

var BadPrivateDTOKey = gordis.NewKey[BadPrivateDTO]("test/BadPrivateDTO")

type BadEmbeddedDTO interface {
	Call(context.Context, struct{ PlainDTO }) error
}

var BadEmbeddedDTOKey = gordis.NewKey[BadEmbeddedDTO]("test/BadEmbeddedDTO")

type BadPrivateField interface {
	Call(context.Context, struct{ value int }) error
}

var BadPrivateFieldKey = gordis.NewKey[BadPrivateField]("test/BadPrivateField")

type BadTag interface {
	Call(context.Context, struct {
		Value int `json:"value,string"`
	}) error
}

var BadTagKey = gordis.NewKey[BadTag]("test/BadTag")

type BadDuplicateTag interface {
	Call(context.Context, struct {
		A int `json:"x"`
		B int `json:"x"`
	}) error
}

var BadDuplicateTagKey = gordis.NewKey[BadDuplicateTag]("test/BadDuplicateTag")

type BadGenericDTO interface {
	Call(context.Context, GenericDTO[int]) error
}

var BadGenericDTOKey = gordis.NewKey[BadGenericDTO]("test/BadGenericDTO")

type BadChunkFunction interface {
	Call(context.Context, func(func(int) error) error) error
}

var BadChunkFunctionKey = gordis.NewKey[BadChunkFunction]("test/BadChunkFunction")

type BadContextPayload interface {
	Call(context.Context, context.Context) error
}

var BadContextPayloadKey = gordis.NewKey[BadContextPayload]("test/BadContextPayload")

type BadResult interface {
	Call(context.Context) (any, error)
}

var BadResultKey = gordis.NewKey[BadResult]("test/BadResult")

type BadChannelResult interface {
	Call(context.Context) (chan int, error)
}

var BadChannelResultKey = gordis.NewKey[BadChannelResult]("test/BadChannelResult")

type PlainDTO struct{ Value int }
