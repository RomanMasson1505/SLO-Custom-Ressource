/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package promclient is a thin wrapper around the Prometheus HTTP API.
//
// The controller depends on the PromAPI interface, not on the concrete Client,
// so tests can inject a fake implementation instead of talking to a real
// Prometheus.
package promclient

import (
	"context"
	"fmt"
	"time"

	api "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// PromAPI is the narrow contract the controller needs: run an instant query and
// get a single number back. Keeping it tiny makes it trivial to fake in tests.
type PromAPI interface {
	QueryScalar(ctx context.Context, query string) (float64, error)
}

// Client is the real PromAPI, backed by the Prometheus HTTP API.
type Client struct {
	api promv1.API
}

// compile-time check that Client satisfies PromAPI.
var _ PromAPI = (*Client)(nil)

// New builds a Client pointing at the given Prometheus base URL. It does not
// connect yet; the first query is what actually hits the network.
func New(url string) (*Client, error) {
	c, err := api.NewClient(api.Config{Address: url})
	if err != nil {
		return nil, fmt.Errorf("creating prometheus client: %w", err)
	}
	return &Client{api: promv1.NewAPI(c)}, nil
}

// QueryScalar runs an instant query at "now" and reduces the result to one
// float. It accepts either a scalar or a single-sample instant vector, which is
// what our error-ratio queries return.
func (c *Client) QueryScalar(ctx context.Context, query string) (float64, error) {
	val, warnings, err := c.api.Query(ctx, query, time.Now())
	if err != nil {
		return 0, fmt.Errorf("querying prometheus: %w", err)
	}
	if len(warnings) > 0 {
		// Non-fatal: log-worthy, but we still trust the value.
		_ = warnings
	}

	switch v := val.(type) {
	case *model.Scalar:
		return float64(v.Value), nil
	case model.Vector:
		if len(v) == 0 {
			return 0, fmt.Errorf("query %q returned no data", query)
		}
		return float64(v[0].Value), nil
	default:
		return 0, fmt.Errorf("query %q returned unsupported type %T", query, val)
	}
}
