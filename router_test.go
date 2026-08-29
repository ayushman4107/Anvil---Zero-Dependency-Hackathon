package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestRouteTreePrecedenceAndCaptures(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/users/new", namedHandler("static"))
	mustRegisterRoute(t, router, "GET", "/users/:id", namedHandler("parameter"))
	mustRegisterRoute(t, router, "GET", "/assets/*path", namedHandler("wildcard"))
	mustRegisterRoute(t, router, anyMethod, "/proxy/*rest", namedHandler("any"))
	if err := router.Freeze(); err != nil {
		t.Fatalf("Freeze() = %v", err)
	}

	tests := []struct {
		name      string
		method    string
		target    string
		wantBody  string
		paramName string
		param     string
	}{
		{name: "static over parameter", method: "GET", target: "/users/new", wantBody: "static"},
		{name: "parameter", method: "GET", target: "/users/alice?verbose=1", wantBody: "parameter", paramName: "id", param: "alice"},
		{name: "wildcard", method: "GET", target: "/assets/css/app.css", wantBody: "wildcard", paramName: "path", param: "css/app.css"},
		{name: "any method", method: "DELETE", target: "/proxy/v1/items", wantBody: "any", paramName: "rest", param: "v1/items"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution := router.Lookup(test.method, test.target)
			if resolution.Handler == nil {
				t.Fatal("Lookup() did not resolve a handler")
			}
			response, err := resolution.Handler(context.Background(), nil, resolution.Params)
			if err != nil || string(response.Body) != test.wantBody {
				t.Fatalf("handler = body %q, error %v; want %q", response.Body, err, test.wantBody)
			}
			if test.paramName != "" {
				value, ok := resolution.Params.Get(test.paramName)
				if !ok || value != test.param {
					t.Fatalf("parameter %q = %q, %v; want %q, true", test.paramName, value, ok, test.param)
				}
			}
		})
	}
}

func TestRouteTreeMethodSeparationAndAllowedMethods(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "POST", "/items", namedHandler("post"))
	mustRegisterRoute(t, router, "GET", "/items", namedHandler("get"))
	mustRegisterRoute(t, router, "PATCH", "/items", namedHandler("patch"))
	if err := router.Freeze(); err != nil {
		t.Fatal(err)
	}

	resolution := router.Lookup("DELETE", "/items?ignored=1")
	if resolution.Handler != nil {
		t.Fatal("DELETE unexpectedly resolved")
	}
	want := []string{"GET", "PATCH", "POST"}
	if fmt.Sprint(resolution.AllowedMethods) != fmt.Sprint(want) {
		t.Fatalf("AllowedMethods = %v, want %v", resolution.AllowedMethods, want)
	}
}

func TestRouteTreeRejectsInvalidAndAmbiguousRegistrations(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/users/:id", namedHandler("first"))
	if err := router.Register("GET", "/users/:name", namedHandler("duplicate shape")); err == nil {
		t.Fatal("structurally duplicate parameter route was accepted")
	}
	invalid := []string{"users", "/a/*rest/more", "/a/:", "/a/*", "/a/:id/:id", "/a?query=1"}
	for _, pattern := range invalid {
		if err := router.Register("GET", pattern, namedHandler("invalid")); err == nil {
			t.Errorf("invalid pattern %q was accepted", pattern)
		}
	}
	if err := router.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := router.Register("POST", "/later", namedHandler("late")); err == nil {
		t.Fatal("registration after Freeze was accepted")
	}
}

func TestRouteTreeKeepsCaptureNamesPerEndpoint(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/users/:id/profile", namedHandler("profile"))
	mustRegisterRoute(t, router, "GET", "/users/:name/settings", namedHandler("settings"))
	if err := router.Freeze(); err != nil {
		t.Fatal(err)
	}
	profile := router.Lookup("GET", "/users/42/profile")
	if value, ok := profile.Params.Get("id"); !ok || value != "42" {
		t.Fatalf("profile id = %q, %v", value, ok)
	}
	settings := router.Lookup("GET", "/users/alice/settings")
	if value, ok := settings.Params.Get("name"); !ok || value != "alice" {
		t.Fatalf("settings name = %q, %v", value, ok)
	}
}

func TestRouteTreeDoesNotDecodePercentEscapes(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/objects/:key", namedHandler("object"))
	if err := router.Freeze(); err != nil {
		t.Fatal(err)
	}
	resolution := router.Lookup("GET", "/objects/a%2Fb")
	if value, ok := resolution.Params.Get("key"); !ok || value != "a%2Fb" {
		t.Fatalf("raw parameter = %q, %v; want a%%2Fb, true", value, ok)
	}
}

func TestRouteTreeConcurrentFrozenLookups(t *testing.T) {
	router := newRouteTree()
	mustRegisterRoute(t, router, "GET", "/v1/:resource/*tail", namedHandler("read"))
	if err := router.Freeze(); err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 500; iteration++ {
				resolution := router.Lookup("GET", "/v1/items/42/history")
				if resolution.Handler == nil {
					t.Errorf("concurrent Lookup() missed route")
					return
				}
			}
		}()
	}
	workers.Wait()
}

func mustRegisterRoute(t *testing.T, router *routeTree, method, pattern string, handler routeHandler) {
	t.Helper()
	if err := router.Register(method, pattern, handler); err != nil {
		t.Fatalf("Register(%q, %q) = %v", method, pattern, err)
	}
}

func namedHandler(name string) routeHandler {
	return func(context.Context, *httpRequest, routeParams) (*httpResponse, error) {
		return textResponse(200, name), nil
	}
}
