package processbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/gordis/process"
)

// EventDirection declares which side effects a process Plugin may initiate for
// one event contract. Host delivery to a remote subscription is covered by
// EventSubscribe; remote entry into the Host Bus is covered by EventPublish.
type EventDirection uint8

const (
	EventPublish EventDirection = 1 << iota
	EventSubscribe
)

// EventCodec is an explicit payload wire. ID identifies the schema/encoding;
// changing an incompatible representation requires a new ID.
type EventCodec[T any] struct {
	ID     string
	Encode func(T) (json.RawMessage, error)
	Decode func(json.RawMessage) (T, error)
}

// JSONEventCodec is an explicit opt-in to encoding/json for one stable wire
// schema. Local-only events never need a codec.
func JSONEventCodec[T any](id string) EventCodec[T] {
	return EventCodec[T]{
		ID: id,
		Encode: func(value T) (json.RawMessage, error) {
			return json.Marshal(value)
		},
		Decode: func(raw json.RawMessage) (T, error) {
			var value T
			err := json.Unmarshal(raw, &value)
			return value, err
		},
	}
}

type eventContractKind uint8

const (
	eventTopic eventContractKind = iota + 1
	eventHook
)

// EventBinding registers the wire representation and allowed directions for
// one Topic or Hook. Construct it with BindTopic or BindHook.
type EventBinding struct {
	id          string
	wire        string
	kind        eventContractKind
	directions  EventDirection
	contract    events.Contract
	validCodec  bool
	encodeInput func(any) (json.RawMessage, error)
	decodeInput func(json.RawMessage) (any, error)
	encodeOut   func(any) (json.RawMessage, error)
	decodeOut   func(json.RawMessage) (any, error)
	makeNext    func(func(context.Context, any) (any, error)) any
	callBase    func(any, context.Context, any) (any, error)
	subscribe   func(events.Binding, events.RemoteHandlerKind, remoteDeliver, []events.OnOption) (func(), error)
}

type remoteDeliver func(
	context.Context,
	any,
	func(context.Context, any) (any, error),
) (events.RemoteResult, error)

func typedCodec[T any](codec EventCodec[T]) (
	func(any) (json.RawMessage, error),
	func(json.RawMessage) (any, error),
) {
	return func(value any) (json.RawMessage, error) {
			if value == nil {
				var zero T
				return codec.Encode(zero)
			}
			typed, ok := value.(T)
			if !ok {
				return nil, errors.New("processbridge: event value has incompatible type")
			}
			return codec.Encode(typed)
		}, func(raw json.RawMessage) (any, error) {
			return codec.Decode(raw)
		}
}

// BindTopic enables selected process directions for one notification Topic.
func BindTopic[T any](
	topic events.Topic[T],
	codec EventCodec[T],
	directions EventDirection,
) EventBinding {
	encode, decode := typedCodec(codec)
	binding := EventBinding{
		id: topic.ID(), kind: eventTopic, directions: directions, contract: topic,
		validCodec:  codec.ID != "" && codec.Encode != nil && codec.Decode != nil,
		encodeInput: encode, decodeInput: decode,
	}
	binding.wire = eventFingerprint(binding.kind, topic.ID(), codec.ID, "", directions)
	binding.subscribe = func(
		bus events.Binding,
		kind events.RemoteHandlerKind,
		deliver remoteDeliver,
		opts []events.OnOption,
	) (func(), error) {
		if kind != events.RemoteNotification {
			return nil, errors.New("processbridge: Topic requires a notification handler")
		}
		return bus.On(topic, func(ctx context.Context, payload T) error {
			_, err := deliver(ctx, payload, nil)
			return err
		}, opts...)
	}
	return binding
}

// BindHook enables selected process directions for one response Hook. Input
// and output codecs are independently versioned.
func BindHook[I, O any](
	hook events.Hook[I, O],
	input EventCodec[I],
	output EventCodec[O],
	directions EventDirection,
) EventBinding {
	encodeInput, decodeInput := typedCodec(input)
	encodeOut, decodeOut := typedCodec(output)
	binding := EventBinding{
		id: hook.ID(), kind: eventHook, directions: directions, contract: hook,
		validCodec: input.ID != "" && input.Encode != nil && input.Decode != nil &&
			output.ID != "" && output.Encode != nil && output.Decode != nil,
		encodeInput: encodeInput, decodeInput: decodeInput,
		encodeOut: encodeOut, decodeOut: decodeOut,
	}
	binding.wire = eventFingerprint(binding.kind, hook.ID(), input.ID, output.ID, directions)
	binding.makeNext = func(call func(context.Context, any) (any, error)) any {
		return events.Next[I, O](func(ctx context.Context, input I) (O, error) {
			result, err := call(ctx, input)
			if result == nil {
				var zero O
				return zero, err
			}
			typed, ok := result.(O)
			if !ok {
				var zero O
				return zero, errors.New("processbridge: event result has incompatible type")
			}
			return typed, err
		})
	}
	binding.callBase = func(base any, ctx context.Context, input any) (any, error) {
		fn, ok := base.(events.Next[I, O])
		if !ok {
			return nil, errors.New("processbridge: waterfall base has incompatible type")
		}
		typed, ok := input.(I)
		if !ok {
			return nil, errors.New("processbridge: waterfall input has incompatible type")
		}
		return fn(ctx, typed)
	}
	binding.subscribe = func(
		bus events.Binding,
		kind events.RemoteHandlerKind,
		deliver remoteDeliver,
		opts []events.OnOption,
	) (func(), error) {
		switch kind {
		case events.RemoteSelection:
			return bus.Handle(hook, func(ctx context.Context, input I) (O, bool, error) {
				result, err := deliver(ctx, input, nil)
				if result.Value == nil {
					var zero O
					return zero, result.Handled, err
				}
				value, ok := result.Value.(O)
				if !ok {
					var zero O
					return zero, false, errors.New("processbridge: selection result has incompatible type")
				}
				return value, result.Handled, err
			}, opts...)
		case events.RemoteMiddleware:
			return bus.Use(hook, func(ctx context.Context, input I, next events.Next[I, O]) (O, error) {
				result, err := deliver(ctx, input, func(nextCtx context.Context, nextInput any) (any, error) {
					typed, ok := nextInput.(I)
					if !ok {
						return nil, errors.New("processbridge: continuation input has incompatible type")
					}
					return next(nextCtx, typed)
				})
				if result.Value == nil {
					var zero O
					return zero, err
				}
				value, ok := result.Value.(O)
				if !ok {
					var zero O
					return zero, errors.New("processbridge: middleware result has incompatible type")
				}
				return value, err
			}, opts...)
		default:
			return nil, errors.New("processbridge: Hook requires a selection or middleware handler")
		}
	}
	return binding
}

func eventFingerprint(kind eventContractKind, id, input, output string, directions EventDirection) string {
	data := fmt.Sprintf("%d:%d:%s:%d:%s:%d:%s:%d", kind, len(id), id, len(input), input, len(output), output, directions)
	sum := sha256.Sum256([]byte(data))
	return "gordis.event/1:" + hex.EncodeToString(sum[:])
}

func (b EventBinding) validate() error {
	if b.id == "" || b.contract == nil || b.contract.ID() != b.id || b.wire == "" || !b.validCodec ||
		b.encodeInput == nil || b.decodeInput == nil || b.subscribe == nil {
		return errors.New("processbridge: invalid event binding")
	}
	if b.directions == 0 || b.directions & ^(EventPublish|EventSubscribe) != 0 {
		return errors.New("processbridge: invalid event direction")
	}
	if b.kind == eventHook && (b.encodeOut == nil || b.decodeOut == nil || b.makeNext == nil || b.callBase == nil) {
		return errors.New("processbridge: invalid Hook binding")
	}
	if b.kind != eventTopic && b.kind != eventHook {
		return errors.New("processbridge: invalid event kind")
	}
	return nil
}

func eventBindings(bindings []EventBinding) (map[string]EventBinding, error) {
	registry := make(map[string]EventBinding, len(bindings))
	for _, binding := range bindings {
		if err := binding.validate(); err != nil {
			return nil, err
		}
		if _, exists := registry[binding.id]; exists {
			return nil, fmt.Errorf("processbridge: duplicate event binding %q", binding.id)
		}
		registry[binding.id] = binding
	}
	return registry, nil
}

func eventContractNames(bindings []EventBinding) []string {
	if len(bindings) == 0 {
		return nil
	}
	names := make([]string, 0, len(bindings)+1)
	wires := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, binding.wire)
		wires = append(wires, binding.wire)
	}
	sort.Strings(wires)
	sum := sha256.Sum256([]byte(strings.Join(wires, "\x00")))
	names = append(names, "gordis.events.registry/1:"+hex.EncodeToString(sum[:]))
	return names
}

const (
	eventSubscribeMethod   = "bridge.event.subscribe"
	eventUnsubscribeMethod = "bridge.event.unsubscribe"
	eventDispatchMethod    = "bridge.event.dispatch"
	eventDeliverMethod     = "bridge.event.deliver"
	eventCallbackMethod    = "bridge.event.callback"
	eventReadyMethod       = "bridge.event.ready"
)

type eventOptions struct {
	Once     bool `json:"once,omitempty"`
	Global   bool `json:"global,omitempty"`
	Priority int  `json:"priority,omitempty"`
}

type eventSubscribeRequest struct {
	LocalID  uint64                   `json:"localId"`
	Contract string                   `json:"contract"`
	Wire     string                   `json:"wire"`
	Kind     events.RemoteHandlerKind `json:"kind"`
	Options  eventOptions             `json:"options"`
}

type eventSubscribeResponse struct {
	HostID uint64 `json:"hostId"`
}

type eventUnsubscribeRequest struct {
	HostID uint64 `json:"hostId"`
}

type eventDispatchRequest struct {
	Contract string                    `json:"contract"`
	Wire     string                    `json:"wire"`
	Mode     events.RemoteDispatchMode `json:"mode"`
	Payload  json.RawMessage           `json:"payload"`
	Source   string                    `json:"source,omitempty"`
	Base     uint64                    `json:"base,omitempty"`
}

type eventDeliverRequest struct {
	HostID   uint64          `json:"hostId"`
	Contract string          `json:"contract"`
	Wire     string          `json:"wire"`
	Payload  json.RawMessage `json:"payload"`
	Next     uint64          `json:"next,omitempty"`
}

type eventCallbackRequest struct {
	Token    uint64          `json:"token"`
	Contract string          `json:"contract"`
	Wire     string          `json:"wire"`
	Payload  json.RawMessage `json:"payload"`
}

type eventWireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type eventResponse struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Handled bool            `json:"handled,omitempty"`
	Error   *eventWireError `json:"error,omitempty"`
}

func encodeEventError(err error) *eventWireError {
	if err == nil {
		return nil
	}
	code := "handler"
	switch {
	case errors.Is(err, context.Canceled):
		code = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "deadline"
	case errors.Is(err, events.ErrNextCalled):
		code = "next_called"
	case errors.Is(err, events.ErrNextExpired):
		code = "next_expired"
	case errors.Is(err, events.ErrNoHandler):
		code = "no_handler"
	case errors.Is(err, process.ErrBackpressure):
		code = "backpressure"
	case errors.Is(err, gordis.ErrClosed):
		code = "closed"
	case errors.Is(err, gordis.ErrStale):
		code = "stale"
	case errors.Is(err, gordis.ErrNotReady):
		code = "not_ready"
	}
	return &eventWireError{Code: code, Message: err.Error()}
}

func decodeEventError(wire *eventWireError) error {
	if wire == nil {
		return nil
	}
	var sentinel error
	switch wire.Code {
	case "canceled":
		sentinel = context.Canceled
	case "deadline":
		sentinel = context.DeadlineExceeded
	case "next_called":
		sentinel = events.ErrNextCalled
	case "next_expired":
		sentinel = events.ErrNextExpired
	case "no_handler":
		sentinel = events.ErrNoHandler
	case "backpressure":
		sentinel = process.ErrBackpressure
	case "closed":
		sentinel = gordis.ErrClosed
	case "stale":
		sentinel = gordis.ErrStale
	case "not_ready":
		sentinel = gordis.ErrNotReady
	}
	message := errors.New(wire.Message)
	if sentinel == nil || wire.Message == sentinel.Error() {
		if sentinel != nil {
			return sentinel
		}
		return message
	}
	return errors.Join(sentinel, message)
}

func eventCallError(err error) error {
	if err == nil {
		return nil
	}
	var rpc *process.RPCError
	if errors.As(err, &rpc) && rpc.Code == "backpressure" {
		return errors.Join(process.ErrBackpressure, err)
	}
	return err
}

type continuation struct {
	binding EventBinding
	call    func(context.Context, any) (any, error)
	used    atomic.Bool
}

type continuationSet struct {
	mu     sync.Mutex
	next   uint64
	values map[uint64]*continuation
}

func (s *continuationSet) add(binding EventBinding, call func(context.Context, any) (any, error)) uint64 {
	if call == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[uint64]*continuation{}
	}
	s.next++
	s.values[s.next] = &continuation{binding: binding, call: call}
	return s.next
}

func (s *continuationSet) remove(token uint64) {
	if token == 0 {
		return
	}
	s.mu.Lock()
	delete(s.values, token)
	s.mu.Unlock()
}

func (s *continuationSet) call(ctx context.Context, request eventCallbackRequest) eventResponse {
	s.mu.Lock()
	item := s.values[request.Token]
	s.mu.Unlock()
	if item == nil || item.binding.id != request.Contract || item.binding.wire != request.Wire {
		return eventResponse{Error: encodeEventError(events.ErrNextExpired)}
	}
	if !item.used.CompareAndSwap(false, true) {
		return eventResponse{Error: encodeEventError(events.ErrNextCalled)}
	}
	input, err := item.binding.decodeInput(request.Payload)
	if err != nil {
		return eventResponse{Error: encodeEventError(err)}
	}
	result, err := item.call(ctx, input)
	response := eventResponse{Error: encodeEventError(err)}
	if err == nil {
		response.Payload, err = item.binding.encodeOut(result)
		response.Error = encodeEventError(err)
	}
	return response
}

func decodeEventResult(binding EventBinding, response eventResponse) (events.RemoteResult, error) {
	if err := decodeEventError(response.Error); err != nil {
		return events.RemoteResult{Handled: response.Handled}, err
	}
	if len(response.Payload) == 0 {
		return events.RemoteResult{Handled: response.Handled}, nil
	}
	if binding.decodeOut == nil {
		return events.RemoteResult{}, errors.New("processbridge: notification returned a payload")
	}
	value, err := binding.decodeOut(response.Payload)
	return events.RemoteResult{Value: value, Handled: response.Handled}, err
}

type eventHost struct {
	mu            sync.Mutex
	scope         *gordis.Scope
	bindings      map[string]EventBinding
	services      map[string]gordis.ServiceSpec
	peer          process.Caller
	closed        bool
	nextHostID    uint64
	subscriptions map[uint64]hostSubscription
	remoteIDs     map[uint64]uint64
	continuations continuationSet
}

type hostSubscription struct {
	remoteID uint64
	cancel   func()
}

func newEventHost(scope *gordis.Scope, bindings []EventBinding, services []Binding) (*eventHost, error) {
	registry, err := eventBindings(bindings)
	if err != nil {
		return nil, err
	}
	serviceRegistry := make(map[string]gordis.ServiceSpec, len(services))
	for _, binding := range services {
		serviceRegistry[binding.spec.Name()] = binding.spec
	}
	return &eventHost{
		scope: scope, bindings: registry, services: serviceRegistry,
		subscriptions: map[uint64]hostSubscription{}, remoteIDs: map[uint64]uint64{},
	}, nil
}

func (h *eventHost) setPeer(peer process.Caller) {
	h.mu.Lock()
	h.peer = peer
	h.mu.Unlock()
}

func (h *eventHost) binding(id, wire string, direction EventDirection) (EventBinding, error) {
	binding, ok := h.bindings[id]
	if !ok || binding.wire != wire || binding.directions&direction == 0 {
		return EventBinding{}, &process.RPCError{Code: "event", Message: "undeclared event capability"}
	}
	return binding, nil
}

func eventOnOptions(options eventOptions) []events.OnOption {
	var out []events.OnOption
	if options.Once {
		out = append(out, events.Once())
	}
	if options.Global {
		out = append(out, events.Global())
	}
	if options.Priority != 0 {
		out = append(out, events.Priority(options.Priority))
	}
	return out
}

func (h *eventHost) subscribe(ctx context.Context, request eventSubscribeRequest) (eventSubscribeResponse, error) {
	if err := ctx.Err(); err != nil {
		return eventSubscribeResponse{}, err
	}
	if request.LocalID == 0 {
		return eventSubscribeResponse{}, errors.New("processbridge: zero remote subscription ID")
	}
	binding, err := h.binding(request.Contract, request.Wire, EventSubscribe)
	if err != nil {
		return eventSubscribeResponse{}, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return eventSubscribeResponse{}, gordis.ErrClosed
	}
	if _, exists := h.remoteIDs[request.LocalID]; exists {
		h.mu.Unlock()
		return eventSubscribeResponse{}, errors.New("processbridge: duplicate remote subscription ID")
	}
	h.nextHostID++
	hostID := h.nextHostID
	h.remoteIDs[request.LocalID] = hostID
	h.mu.Unlock()
	cancel, err := binding.subscribe(
		events.Bind(h.scope), request.Kind,
		func(deliveryCtx context.Context, payload any, next func(context.Context, any) (any, error)) (events.RemoteResult, error) {
			return h.deliver(deliveryCtx, hostID, binding, payload, next)
		},
		eventOnOptions(request.Options),
	)
	if err != nil {
		h.mu.Lock()
		delete(h.remoteIDs, request.LocalID)
		h.mu.Unlock()
		return eventSubscribeResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		h.mu.Lock()
		delete(h.remoteIDs, request.LocalID)
		h.mu.Unlock()
		cancel()
		return eventSubscribeResponse{}, err
	}
	h.mu.Lock()
	if h.closed {
		delete(h.remoteIDs, request.LocalID)
		h.mu.Unlock()
		cancel()
		return eventSubscribeResponse{}, gordis.ErrClosed
	}
	h.subscriptions[hostID] = hostSubscription{remoteID: request.LocalID, cancel: cancel}
	h.mu.Unlock()
	return eventSubscribeResponse{HostID: hostID}, nil
}

func (h *eventHost) unsubscribe(hostID uint64) {
	h.mu.Lock()
	subscription := h.subscriptions[hostID]
	delete(h.subscriptions, hostID)
	delete(h.remoteIDs, subscription.remoteID)
	h.mu.Unlock()
	if subscription.cancel != nil {
		subscription.cancel()
	}
}

func (h *eventHost) close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	cancels := make([]func(), 0, len(h.subscriptions))
	for _, subscription := range h.subscriptions {
		cancels = append(cancels, subscription.cancel)
	}
	h.subscriptions = map[uint64]hostSubscription{}
	h.remoteIDs = map[uint64]uint64{}
	h.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (h *eventHost) ready(ctx context.Context) error {
	h.mu.Lock()
	peer, closed := h.peer, h.closed
	h.mu.Unlock()
	if closed || peer == nil {
		return gordis.ErrClosed
	}
	return eventCallError(peer.Call(ctx, eventReadyMethod, nil, nil))
}

func (h *eventHost) deliver(
	ctx context.Context,
	hostID uint64,
	binding EventBinding,
	payload any,
	next func(context.Context, any) (any, error),
) (events.RemoteResult, error) {
	raw, err := binding.encodeInput(payload)
	if err != nil {
		return events.RemoteResult{}, err
	}
	token := h.continuations.add(binding, next)
	defer h.continuations.remove(token)
	h.mu.Lock()
	peer, closed := h.peer, h.closed
	h.mu.Unlock()
	if closed || peer == nil {
		return events.RemoteResult{}, gordis.ErrClosed
	}
	var response eventResponse
	err = peer.Call(ctx, eventDeliverMethod, eventDeliverRequest{
		HostID: hostID, Contract: binding.id, Wire: binding.wire, Payload: raw, Next: token,
	}, &response)
	if err != nil {
		return events.RemoteResult{}, eventCallError(err)
	}
	return decodeEventResult(binding, response)
}

func (h *eventHost) remoteCallback(
	ctx context.Context,
	peer process.Caller,
	binding EventBinding,
	token uint64,
	input any,
) (any, error) {
	raw, err := binding.encodeInput(input)
	if err != nil {
		return nil, err
	}
	var response eventResponse
	if err := peer.Call(ctx, eventCallbackMethod, eventCallbackRequest{
		Token: token, Contract: binding.id, Wire: binding.wire, Payload: raw,
	}, &response); err != nil {
		return nil, eventCallError(err)
	}
	result, err := decodeEventResult(binding, response)
	return result.Value, err
}

func (h *eventHost) dispatch(
	ctx context.Context,
	peer process.Caller,
	request eventDispatchRequest,
) eventResponse {
	binding, err := h.binding(request.Contract, request.Wire, EventPublish)
	if err != nil {
		return eventResponse{Error: encodeEventError(err)}
	}
	payload, err := binding.decodeInput(request.Payload)
	if err != nil {
		return eventResponse{Error: encodeEventError(err)}
	}
	var source *gordis.ServiceSpec
	if request.Source != "" {
		service, ok := h.services[request.Source]
		if !ok {
			return eventResponse{Error: encodeEventError(errors.New("processbridge: undeclared event source service"))}
		}
		source = &service
	}
	var base any
	if request.Mode == events.RemoteWaterfall {
		if binding.kind != eventHook || request.Base == 0 {
			return eventResponse{Error: encodeEventError(errors.New("processbridge: waterfall needs a remote base"))}
		}
		base = binding.makeNext(func(callCtx context.Context, input any) (any, error) {
			return h.remoteCallback(callCtx, peer, binding, request.Base, input)
		})
	}
	result, err := events.Bind(h.scope).DispatchRemote(ctx, events.RemoteDispatch{
		Contract: binding.contract, Mode: request.Mode, Payload: payload, Source: source, Base: base,
	})
	response := eventResponse{Handled: result.Handled, Error: encodeEventError(err)}
	if err == nil && result.Value != nil {
		response.Payload, err = binding.encodeOut(result.Value)
		response.Error = encodeEventError(err)
	}
	return response
}

func (h *eventHost) handle(
	ctx context.Context,
	peer process.Caller,
	method string,
	raw json.RawMessage,
) (any, error) {
	switch method {
	case eventSubscribeMethod:
		var request eventSubscribeRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		return h.subscribe(ctx, request)
	case eventUnsubscribeMethod:
		var request eventUnsubscribeRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		h.unsubscribe(request.HostID)
		return nil, nil
	case eventDispatchMethod:
		var request eventDispatchRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		return h.dispatch(ctx, peer, request), nil
	case eventCallbackMethod:
		var request eventCallbackRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		return h.continuations.call(ctx, request), nil
	default:
		return nil, &process.RPCError{Code: "method", Message: "unknown event bridge method"}
	}
}

type remoteEventTransport struct {
	peer          process.Caller
	bindings      map[string]EventBinding
	endpoint      events.RemoteEndpoint
	continuations continuationSet
	ready         chan struct{}
	readyOnce     sync.Once
}

func newRemoteEventTransport(peer process.Caller, bindings []EventBinding) (*remoteEventTransport, error) {
	registry, err := eventBindings(bindings)
	if err != nil {
		return nil, err
	}
	transport := &remoteEventTransport{peer: peer, bindings: registry, ready: make(chan struct{})}
	transport.endpoint = events.NewRemoteBus(transport)
	return transport, nil
}

func (t *remoteEventTransport) binding(id string, direction EventDirection) (EventBinding, error) {
	binding, ok := t.bindings[id]
	if !ok || binding.directions&direction == 0 {
		return EventBinding{}, errors.New("processbridge: undeclared event capability")
	}
	return binding, nil
}

func (t *remoteEventTransport) Subscribe(
	ctx context.Context,
	subscription events.RemoteSubscription,
) (uint64, error) {
	binding, err := t.binding(subscription.Contract.ID(), EventSubscribe)
	if err != nil {
		return 0, err
	}
	if !events.SameContract(binding.contract, subscription.Contract) {
		return 0, errors.New("processbridge: local event contract differs from its wire binding")
	}
	var response eventSubscribeResponse
	err = t.peer.Call(ctx, eventSubscribeMethod, eventSubscribeRequest{
		LocalID: subscription.LocalID, Contract: binding.id, Wire: binding.wire,
		Kind: subscription.Kind, Options: eventOptions{
			Once: subscription.Options.Once, Global: subscription.Options.Global,
			Priority: subscription.Options.Priority,
		},
	}, &response)
	return response.HostID, eventCallError(err)
}

func (t *remoteEventTransport) Unsubscribe(ctx context.Context, hostID uint64) error {
	return eventCallError(t.peer.Call(ctx, eventUnsubscribeMethod, eventUnsubscribeRequest{HostID: hostID}, nil))
}

func (t *remoteEventTransport) Dispatch(
	ctx context.Context,
	request events.RemoteDispatch,
) (events.RemoteResult, error) {
	select {
	case <-t.ready:
	case <-ctx.Done():
		return events.RemoteResult{}, ctx.Err()
	}
	binding, err := t.binding(request.Contract.ID(), EventPublish)
	if err != nil {
		return events.RemoteResult{}, err
	}
	if !events.SameContract(binding.contract, request.Contract) {
		return events.RemoteResult{}, errors.New("processbridge: local event contract differs from its wire binding")
	}
	payload, err := binding.encodeInput(request.Payload)
	if err != nil {
		return events.RemoteResult{}, err
	}
	var source string
	if request.Source != nil {
		source = request.Source.Name()
	}
	var base uint64
	if request.Mode == events.RemoteWaterfall {
		if binding.callBase == nil || request.Base == nil {
			return events.RemoteResult{}, errors.New("processbridge: waterfall needs a base")
		}
		base = t.continuations.add(binding, func(callCtx context.Context, input any) (any, error) {
			return binding.callBase(request.Base, callCtx, input)
		})
		defer t.continuations.remove(base)
	}
	var response eventResponse
	err = t.peer.Call(ctx, eventDispatchMethod, eventDispatchRequest{
		Contract: binding.id, Wire: binding.wire, Mode: request.Mode, Payload: payload,
		Source: source, Base: base,
	}, &response)
	if err != nil {
		return events.RemoteResult{}, eventCallError(err)
	}
	return decodeEventResult(binding, response)
}

func (t *remoteEventTransport) next(
	ctx context.Context,
	binding EventBinding,
	token uint64,
	input any,
) (any, error) {
	raw, err := binding.encodeInput(input)
	if err != nil {
		return nil, err
	}
	var response eventResponse
	if err := t.peer.Call(ctx, eventCallbackMethod, eventCallbackRequest{
		Token: token, Contract: binding.id, Wire: binding.wire, Payload: raw,
	}, &response); err != nil {
		return nil, eventCallError(err)
	}
	result, err := decodeEventResult(binding, response)
	return result.Value, err
}

func (t *remoteEventTransport) handle(
	ctx context.Context,
	_ process.Caller,
	method string,
	raw json.RawMessage,
) (any, error) {
	switch method {
	case eventReadyMethod:
		t.readyOnce.Do(func() { close(t.ready) })
		return nil, nil
	case eventDeliverMethod:
		var request eventDeliverRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		binding, err := t.binding(request.Contract, EventSubscribe)
		if err != nil || binding.wire != request.Wire {
			if err == nil {
				err = errors.New("processbridge: event wire mismatch")
			}
			return eventResponse{Error: encodeEventError(err)}, nil
		}
		payload, err := binding.decodeInput(request.Payload)
		if err != nil {
			return eventResponse{Error: encodeEventError(err)}, nil
		}
		var next func(context.Context, any) (any, error)
		if request.Next != 0 {
			next = func(nextCtx context.Context, input any) (any, error) {
				return t.next(nextCtx, binding, request.Next, input)
			}
		}
		result, err := t.endpoint.Deliver(ctx, request.HostID, payload, next)
		response := eventResponse{Handled: result.Handled, Error: encodeEventError(err)}
		if err == nil && result.Value != nil {
			response.Payload, err = binding.encodeOut(result.Value)
			response.Error = encodeEventError(err)
		}
		return response, nil
	case eventCallbackMethod:
		var request eventCallbackRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		return t.continuations.call(ctx, request), nil
	default:
		return nil, &process.RPCError{Code: "method", Message: "unknown event bridge method"}
	}
}

func bridgeHandler(
	services map[string]process.CallHandler,
	eventsHandler process.CallHandler,
) process.CallHandler {
	serviceHandler := router(services)
	return func(ctx context.Context, peer process.Caller, method string, raw json.RawMessage) (any, error) {
		if len(method) >= len("bridge.event.") && method[:len("bridge.event.")] == "bridge.event." {
			if eventsHandler == nil {
				return nil, &process.RPCError{Code: "event", Message: "event bridge is unavailable"}
			}
			return eventsHandler(ctx, peer, method, raw)
		}
		return serviceHandler(ctx, peer, method, raw)
	}
}

func eventBusRequirement(requires []gordis.ServiceSpec, bindings []EventBinding) ([]gordis.ServiceSpec, error) {
	if _, err := eventBindings(bindings); err != nil {
		return nil, err
	}
	out := append([]gordis.ServiceSpec(nil), requires...)
	if len(bindings) == 0 {
		return out, nil
	}
	for _, service := range out {
		if service.Name() == events.BusKey.Name() {
			return nil, errors.New("processbridge: EventBus uses Events, not a service Binding")
		}
	}
	return append(out, events.BusKey.Spec()), nil
}
