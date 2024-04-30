package jwkset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"golang.org/x/time/rate"
)

var (
	// ErrNewClient fails to create a new JWK Set client.
	ErrNewClient = errors.New("failed to create new JWK Set client")
)

// HTTPClientOptions are options for creating a new JWK Set client.
type HTTPClientOptions struct {
	// Given contains keys known from outside HTTP URLs.
	Given Storage
	// HTTPURLs are a mapping of HTTP URLs to JWK Set endpoints to storage implementations for the keys located at the
	// URL. If empty, HTTP will not be used.
	HTTPURLs map[string]*HTTPStorage
	// PrioritizeHTTP is a flag that indicates whether keys from the HTTP URL should be prioritized over keys from the
	// given storage.
	PrioritizeHTTP bool
	// RateLimitWaitMax is the timeout for waiting for rate limiting to end.
	RateLimitWaitMax time.Duration
	// RefreshUnknownKID is non-nil to indicate that remote HTTP resources should be refreshed if a key with an unknown
	// key ID is trying to be read. This makes reading methods block until the context is over, a key with the matching
	// key ID is found in a refreshed remote resource, or all refreshes complete.
	RefreshUnknownKID *rate.Limiter

	storageOptions []HTTPClientStorageOption
}

// HTTPClient is a JWK Set client.
type HTTPClient struct {
	given             Storage
	httpURLs          map[string]*HTTPStorage
	prioritizeHTTP    bool
	rateLimitWaitMax  time.Duration
	refreshUnknownKID *rate.Limiter
}

// NewHTTPClient creates a new JWK Set client from remote HTTP resources.
func NewHTTPClient(options HTTPClientOptions) (*HTTPClient, error) {
	if options.Given == nil && len(options.HTTPURLs) == 0 {
		return nil, fmt.Errorf("%w: no given keys or HTTP URLs", ErrNewClient)
	}
	for u, store := range options.HTTPURLs {
		if store == nil {
			parsed, err := url.ParseRequestURI(u)
			if err != nil {
				return nil, fmt.Errorf("failed to parse given URL %q: %w", u, errors.Join(err, ErrNewClient))
			}
			options.HTTPURLs[u], err = NewStorageFromHTTP(nil, HTTPClientStorageOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to create HTTP client storage for %q: %w", parsed.String(), errors.Join(err, ErrNewClient))
			}
		}
	}
	given := options.Given
	if given == nil {
		given = NewMemoryStorage()
	}
	c := &HTTPClient{
		given:             given,
		httpURLs:          options.HTTPURLs,
		prioritizeHTTP:    options.PrioritizeHTTP,
		rateLimitWaitMax:  options.RateLimitWaitMax,
		refreshUnknownKID: options.RefreshUnknownKID,
	}
	return c, nil
}

// NewDefaultHTTPClient creates a new JWK Set client with default options from remote HTTP resources.
//
// The default behavior is to:
// 1. Refresh remote HTTP resources every hour.
// 2. Prioritize keys from remote HTTP resources over keys from the given storage.
// 3. Refresh remote HTTP resources if a key with an unknown key ID is trying to be read, with a rate limit of 5 minutes.
// 4. Log to slog.Default() if a refresh fails.
func NewDefaultHTTPClient(urls []string, opts ...HTTPClientOption) (Storage, error) {
	return NewDefaultHTTPClientCtx(context.Background(), urls, opts...)
}

// NewDefaultHTTPClientCtx is the same as NewDefaultHTTPClient, but with a context that can end the refresh goroutine.
func NewDefaultHTTPClientCtx(ctx context.Context, urls []string, opts ...HTTPClientOption) (*HTTPClient, error) {
	clientOptions := &HTTPClientOptions{
		HTTPURLs:          make(map[string]*HTTPStorage),
		RateLimitWaitMax:  time.Minute,
		RefreshUnknownKID: rate.NewLimiter(rate.Every(5*time.Minute), 1),
	}
	for _, opt := range opts {
		opt(clientOptions)
	}
	for _, u := range urls {
		parsed, err := url.ParseRequestURI(u)
		if err != nil {
			return nil, fmt.Errorf("failed to parse given URL %q: %w", u, errors.Join(err, ErrNewClient))
		}
		u = parsed.String()
		refreshErrorHandler := func(ctx context.Context, err error) {
			slog.Default().ErrorContext(ctx, "Failed to refresh HTTP JWK Set from remote HTTP resource.",
				"error", err,
				"url", u,
			)
		}
		options := &HTTPClientStorageOptions{
			Ctx:                       ctx,
			NoErrorReturnFirstHTTPReq: true,
			RefreshErrorHandler:       refreshErrorHandler,
			RefreshInterval:           time.Hour,
		}
		for _, opt := range clientOptions.storageOptions {
			opt(options)
		}
		c, err := NewStorageFromHTTP(parsed, *options)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP client storage for %q: %w", u, errors.Join(err, ErrNewClient))
		}
		clientOptions.HTTPURLs[u] = c
	}
	return NewHTTPClient(*clientOptions)
}

func (c *HTTPClient) KeyDelete(ctx context.Context, keyID string) (ok bool, err error) {
	ok, err = c.given.KeyDelete(ctx, keyID)
	if err != nil && !errors.Is(err, ErrKeyNotFound) {
		return false, fmt.Errorf("failed to delete key with ID %q from given storage due to error: %w", keyID, err)
	}
	if ok {
		return true, nil
	}
	for _, store := range c.httpURLs {
		ok, err = store.KeyDelete(ctx, keyID)
		if err != nil && !errors.Is(err, ErrKeyNotFound) {
			return false, fmt.Errorf("failed to delete key with ID %q from HTTP storage due to error: %w", keyID, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
func (c *HTTPClient) KeyRead(ctx context.Context, keyID string) (jwk JWK, err error) {
	if !c.prioritizeHTTP {
		jwk, err = c.given.KeyRead(ctx, keyID)
		switch {
		case errors.Is(err, ErrKeyNotFound):
			// Do nothing.
		case err != nil:
			return JWK{}, fmt.Errorf("failed to find JWT key with ID %q in given storage due to error: %w", keyID, err)
		default:
			return jwk, nil
		}
	}
	for _, store := range c.httpURLs {
		jwk, err = store.KeyRead(ctx, keyID)
		switch {
		case errors.Is(err, ErrKeyNotFound):
			continue
		case err != nil:
			return JWK{}, fmt.Errorf("failed to find JWT key with ID %q in HTTP storage due to error: %w", keyID, err)
		default:
			return jwk, nil
		}
	}
	if c.prioritizeHTTP {
		jwk, err = c.given.KeyRead(ctx, keyID)
		switch {
		case errors.Is(err, ErrKeyNotFound):
			// Do nothing.
		case err != nil:
			return JWK{}, fmt.Errorf("failed to find JWT key with ID %q in given storage due to error: %w", keyID, err)
		default:
			return jwk, nil
		}
	}
	if c.refreshUnknownKID != nil {
		var cancel context.CancelFunc = func() {}
		if c.rateLimitWaitMax > 0 {
			ctx, cancel = context.WithTimeout(ctx, c.rateLimitWaitMax)
		}
		defer cancel()
		err = c.refreshUnknownKID.Wait(ctx)
		if err != nil {
			return JWK{}, fmt.Errorf("failed to wait for JWK Set refresh rate limiter due to error: %w", err)
		}
		for _, store := range c.httpURLs {
			err = store.refresh(ctx)
			if err != nil {
				if store.options.RefreshErrorHandler != nil {
					store.options.RefreshErrorHandler(ctx, err)
				}
				continue
			}
			jwk, err = store.KeyRead(ctx, keyID)
			switch {
			case errors.Is(err, ErrKeyNotFound):
				// Do nothing.
			case err != nil:
				return JWK{}, fmt.Errorf("failed to find JWT key with ID %q in HTTP storage due to error: %w", keyID, err)
			default:
				return jwk, nil
			}
		}
	}
	return JWK{}, fmt.Errorf("%w %q", ErrKeyNotFound, keyID)
}
func (c *HTTPClient) KeyReadAll(ctx context.Context) ([]JWK, error) {
	jwks, err := c.given.KeyReadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot given keys due to error: %w", err)
	}
	for u, store := range c.httpURLs {
		j, err := store.KeyReadAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to snapshot HTTP keys from %q due to error: %w", u, err)
		}
		jwks = append(jwks, j...)
	}
	return jwks, nil
}
func (c *HTTPClient) KeyWrite(ctx context.Context, jwk JWK) error {
	return c.given.KeyWrite(ctx, jwk)
}

func (c *HTTPClient) JSON(ctx context.Context) (json.RawMessage, error) {
	m, err := c.combineStorage(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to combine storage due to error: %w", err)
	}
	return m.JSON(ctx)
}
func (c *HTTPClient) JSONPublic(ctx context.Context) (json.RawMessage, error) {
	m, err := c.combineStorage(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to combine storage due to error: %w", err)
	}
	return m.JSONPublic(ctx)
}
func (c *HTTPClient) JSONPrivate(ctx context.Context) (json.RawMessage, error) {
	m, err := c.combineStorage(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to combine storage due to error: %w", err)
	}
	return m.JSONPrivate(ctx)
}
func (c *HTTPClient) JSONWithOptions(ctx context.Context, marshalOptions JWKMarshalOptions, validationOptions JWKValidateOptions) (json.RawMessage, error) {
	m, err := c.combineStorage(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to combine storage due to error: %w", err)
	}
	return m.JSONWithOptions(ctx, marshalOptions, validationOptions)
}
func (c *HTTPClient) Marshal(ctx context.Context) (JWKSMarshal, error) {
	m, err := c.combineStorage(ctx)
	if err != nil {
		return JWKSMarshal{}, fmt.Errorf("failed to combine storage due to error: %w", err)
	}
	return m.Marshal(ctx)
}
func (c *HTTPClient) MarshalWithOptions(ctx context.Context, marshalOptions JWKMarshalOptions, validationOptions JWKValidateOptions) (JWKSMarshal, error) {
	m, err := c.combineStorage(ctx)
	if err != nil {
		return JWKSMarshal{}, fmt.Errorf("failed to combine storage due to error: %w", err)
	}
	return m.MarshalWithOptions(ctx, marshalOptions, validationOptions)
}

func (c *HTTPClient) combineStorage(ctx context.Context) (Storage, error) {
	jwks, err := c.KeyReadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot keys due to error: %w", err)
	}
	m := NewMemoryStorage()
	for _, jwk := range jwks {
		err = m.KeyWrite(ctx, jwk)
		if err != nil {
			return nil, fmt.Errorf("failed to write key to memory storage due to error: %w", err)
		}
	}
	return m, nil
}
