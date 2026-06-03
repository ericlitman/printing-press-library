// Copyright 2026 Chris Rodriguez and contributors. Licensed under Apache-2.0. See LICENSE.

package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/printing-press-library/library/payments/stripe/internal/config"
)

func TestPostSendsStripeFormEncodedBody(t *testing.T) {
	var gotContentType string
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(&config.Config{BaseURL: srv.URL, StripeSecretKey: "sk_test_form"}, time.Second, 0)
	body := map[string]any{
		"name": "Juno Test",
		"line_items": []any{
			map[string]any{"price": "price_123", "quantity": 1},
		},
		"after_completion": map[string]any{
			"type": "redirect",
			"redirect": map[string]any{
				"url": "https://example.com/done",
			},
		},
		"expand": []any{"data.price.product", "customer"},
	}

	if _, _, err := c.Post("/v1/products", body); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
	}
	if strings.HasPrefix(strings.TrimSpace(gotBody), "{") {
		t.Fatalf("body was JSON, want form encoding: %s", gotBody)
	}

	values, err := url.ParseQuery(gotBody)
	if err != nil {
		t.Fatalf("parse form body: %v", err)
	}
	assertFormValue(t, values, "name", "Juno Test")
	assertFormValue(t, values, "line_items[0][price]", "price_123")
	assertFormValue(t, values, "line_items[0][quantity]", "1")
	assertFormValue(t, values, "after_completion[type]", "redirect")
	assertFormValue(t, values, "after_completion[redirect][url]", "https://example.com/done")
	if got := values["expand[]"]; len(got) != 2 || got[0] != "data.price.product" || got[1] != "customer" {
		t.Fatalf("expand[] = %#v, want repeated scalar array values", got)
	}
}

func TestGetDoesNotSendFormBody(t *testing.T) {
	var gotContentType string
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	c := New(&config.Config{BaseURL: srv.URL, StripeSecretKey: "sk_test_get"}, time.Second, 0)
	if _, err := c.Get("/v1/products", map[string]string{"limit": "1"}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotContentType != "" {
		t.Fatalf("GET Content-Type = %q, want empty", gotContentType)
	}
	if gotBody != "" {
		t.Fatalf("GET body = %q, want empty", gotBody)
	}
}

func assertFormValue(t *testing.T, values url.Values, key, want string) {
	t.Helper()
	if got := values.Get(key); got != want {
		t.Fatalf("%s = %q, want %q (all values: %#v)", key, got, want, values[key])
	}
}
