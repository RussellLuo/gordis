package events

// OnOption configures one subscription.
type OnOption interface{ apply(*subscriptionOptions) }

type option func(*subscriptionOptions)

func (o option) apply(opts *subscriptionOptions) { o(opts) }

type subscriptionOptions struct {
	once     bool
	global   bool
	priority int
}

// Once removes the subscription immediately before its first admitted call.
func Once() OnOption { return option(func(opts *subscriptionOptions) { opts.once = true }) }

// Global bypasses PublishFrom's source-Service filter. It never bypasses the
// Topic partition selected by the subscriber's View.
func Global() OnOption { return option(func(opts *subscriptionOptions) { opts.global = true }) }

// Priority orders higher values before lower values. Equal priorities retain
// registration order.
func Priority(priority int) OnOption {
	return option(func(opts *subscriptionOptions) { opts.priority = priority })
}
