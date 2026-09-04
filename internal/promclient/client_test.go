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

package promclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeProm returns an httptest server that answers /api/v1/query with a canned body.
func fakeProm(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestQueryScalar_Vector(t *testing.T) {
	srv := fakeProm(t, http.StatusOK,
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"0.5"]}]}}`)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.QueryScalar(context.Background(), "up")
	if err != nil {
		t.Fatalf("QueryScalar: %v", err)
	}
	if got != 0.5 {
		t.Errorf("got %v, want 0.5", got)
	}
}

func TestQueryScalar_Scalar(t *testing.T) {
	srv := fakeProm(t, http.StatusOK,
		`{"status":"success","data":{"resultType":"scalar","result":[1700000000,"0.25"]}}`)
	c, _ := New(srv.URL)
	got, err := c.QueryScalar(context.Background(), "scalar(up)")
	if err != nil {
		t.Fatalf("QueryScalar: %v", err)
	}
	if got != 0.25 {
		t.Errorf("got %v, want 0.25", got)
	}
}

func TestQueryScalar_EmptyVector(t *testing.T) {
	srv := fakeProm(t, http.StatusOK,
		`{"status":"success","data":{"resultType":"vector","result":[]}}`)
	c, _ := New(srv.URL)
	if _, err := c.QueryScalar(context.Background(), "up"); err == nil {
		t.Fatal("expected an error for an empty result, got nil")
	}
}

func TestQueryScalar_ServerError(t *testing.T) {
	srv := fakeProm(t, http.StatusBadRequest,
		`{"status":"error","errorType":"bad_data","error":"boom"}`)
	c, _ := New(srv.URL)
	if _, err := c.QueryScalar(context.Background(), "up"); err == nil {
		t.Fatal("expected an error from the server, got nil")
	}
}
