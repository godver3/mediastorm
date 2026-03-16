package datastore

import "time"

type options struct {
	maxConns        int
	minConns        int
	maxConnLifetime time.Duration
}

// Option configures the DataStore.
type Option func(*options)

func defaultOptions() options {
	return options{
		maxConns:        25,
		minConns:        5,
		maxConnLifetime: 30 * time.Minute,
	}
}

// WithMaxConns sets the maximum number of connections in the pool.
func WithMaxConns(n int) Option {
	return func(o *options) { o.maxConns = n }
}

// WithMinConns sets the minimum number of idle connections in the pool.
func WithMinConns(n int) Option {
	return func(o *options) { o.minConns = n }
}

// WithMaxConnLifetime sets the maximum lifetime of a connection.
func WithMaxConnLifetime(d time.Duration) Option {
	return func(o *options) { o.maxConnLifetime = d }
}
