package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const anyMethod = "*"

type routeHandler func(context.Context, *httpRequest, routeParams) (*httpResponse, error)

type routeParam struct {
	Name  string
	Value string
}

type routeParams []routeParam

func (p routeParams) Get(name string) (string, bool) {
	for _, parameter := range p {
		if parameter.Name == name {
			return parameter.Value, true
		}
	}
	return "", false
}

type routeEndpoint struct {
	handler    routeHandler
	paramNames []string
}

type routeNode struct {
	static    map[string]*routeNode
	parameter *routeNode
	wildcard  *routeNode
	endpoint  *routeEndpoint
}

type routeTree struct {
	methods map[string]*routeNode
	frozen  bool
}

type routeResolution struct {
	Handler        routeHandler
	Params         routeParams
	AllowedMethods []string
}

func newRouteTree() *routeTree {
	return &routeTree{methods: make(map[string]*routeNode)}
}

func (r *routeTree) Register(method, pattern string, handler routeHandler) error {
	if r == nil {
		return fmt.Errorf("route tree is required")
	}
	if r.frozen {
		return fmt.Errorf("route tree is frozen")
	}
	if handler == nil {
		return fmt.Errorf("route handler is required")
	}
	if method != anyMethod && !validToken([]byte(method)) {
		return fmt.Errorf("route method %q is not an HTTP token", method)
	}

	segments, parameterNames, kinds, err := parseRoutePattern(pattern)
	if err != nil {
		return err
	}
	root := r.methods[method]
	if root == nil {
		root = &routeNode{}
		r.methods[method] = root
	}
	node := root
	for index, segment := range segments {
		switch kinds[index] {
		case routeSegmentStatic:
			if node.static == nil {
				node.static = make(map[string]*routeNode)
			}
			child := node.static[segment]
			if child == nil {
				child = &routeNode{}
				node.static[segment] = child
			}
			node = child
		case routeSegmentParameter:
			if node.parameter == nil {
				node.parameter = &routeNode{}
			}
			node = node.parameter
		case routeSegmentWildcard:
			if node.wildcard == nil {
				node.wildcard = &routeNode{}
			}
			node = node.wildcard
		}
	}
	if node.endpoint != nil {
		return fmt.Errorf("route %s %s duplicates an existing structural pattern", method, pattern)
	}
	node.endpoint = &routeEndpoint{handler: handler, paramNames: parameterNames}
	return nil
}

func (r *routeTree) Freeze() error {
	if r == nil {
		return fmt.Errorf("route tree is required")
	}
	if len(r.methods) == 0 {
		return fmt.Errorf("at least one route is required")
	}
	r.frozen = true
	return nil
}

func (r *routeTree) Lookup(method, target string) routeResolution {
	if r == nil || !r.frozen {
		return routeResolution{}
	}
	segments := splitRoutePath(requestPath(target))
	if endpoint, values := matchRoute(r.methods[method], segments, 0, nil); endpoint != nil {
		return routeResolution{Handler: endpoint.handler, Params: bindRouteParams(endpoint.paramNames, values)}
	}
	if endpoint, values := matchRoute(r.methods[anyMethod], segments, 0, nil); endpoint != nil {
		return routeResolution{Handler: endpoint.handler, Params: bindRouteParams(endpoint.paramNames, values)}
	}

	allowed := make([]string, 0, len(r.methods))
	for candidate, root := range r.methods {
		if candidate == anyMethod || candidate == method {
			continue
		}
		if endpoint, _ := matchRoute(root, segments, 0, nil); endpoint != nil {
			allowed = append(allowed, candidate)
		}
	}
	sort.Strings(allowed)
	return routeResolution{AllowedMethods: allowed}
}

type routeSegmentKind uint8

const (
	routeSegmentStatic routeSegmentKind = iota
	routeSegmentParameter
	routeSegmentWildcard
)

func parseRoutePattern(pattern string) ([]string, []string, []routeSegmentKind, error) {
	if pattern != "*" && !strings.HasPrefix(pattern, "/") {
		return nil, nil, nil, fmt.Errorf("route pattern %q must be origin-form or *", pattern)
	}
	if strings.ContainsAny(pattern, "?#") {
		return nil, nil, nil, fmt.Errorf("route pattern %q must not contain a query or fragment", pattern)
	}
	segments := splitRoutePath(pattern)
	names := make([]string, 0, len(segments))
	kinds := make([]routeSegmentKind, len(segments))
	seenNames := make(map[string]struct{})
	for index, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			name := strings.TrimPrefix(segment, ":")
			if !validToken([]byte(name)) {
				return nil, nil, nil, fmt.Errorf("route parameter %q has an invalid name", segment)
			}
			if _, exists := seenNames[name]; exists {
				return nil, nil, nil, fmt.Errorf("route parameter name %q is repeated", name)
			}
			seenNames[name] = struct{}{}
			names = append(names, name)
			kinds[index] = routeSegmentParameter
			continue
		}
		if strings.HasPrefix(segment, "*") {
			name := strings.TrimPrefix(segment, "*")
			if index != len(segments)-1 {
				return nil, nil, nil, fmt.Errorf("route wildcard must be terminal")
			}
			if !validToken([]byte(name)) {
				return nil, nil, nil, fmt.Errorf("route wildcard %q has an invalid name", segment)
			}
			if _, exists := seenNames[name]; exists {
				return nil, nil, nil, fmt.Errorf("route parameter name %q is repeated", name)
			}
			seenNames[name] = struct{}{}
			names = append(names, name)
			kinds[index] = routeSegmentWildcard
			continue
		}
		if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*") {
			return nil, nil, nil, fmt.Errorf("invalid route segment %q", segment)
		}
		kinds[index] = routeSegmentStatic
	}
	return segments, names, kinds, nil
}

func requestPath(target string) string {
	if query := strings.IndexByte(target, '?'); query >= 0 {
		return target[:query]
	}
	return target
}

func splitRoutePath(path string) []string {
	if path == "/" {
		return nil
	}
	if path == "*" {
		return []string{"*"}
	}
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

func matchRoute(node *routeNode, segments []string, index int, values []string) (*routeEndpoint, []string) {
	if node == nil {
		return nil, nil
	}
	if index == len(segments) {
		if node.endpoint != nil {
			return node.endpoint, values
		}
		return nil, nil
	}
	segment := segments[index]
	if child := node.static[segment]; child != nil {
		if endpoint, matched := matchRoute(child, segments, index+1, values); endpoint != nil {
			return endpoint, matched
		}
	}
	if segment != "" && node.parameter != nil {
		withValue := appendRouteValue(values, segment)
		if endpoint, matched := matchRoute(node.parameter, segments, index+1, withValue); endpoint != nil {
			return endpoint, matched
		}
	}
	if node.wildcard != nil && node.wildcard.endpoint != nil {
		return node.wildcard.endpoint, appendRouteValue(values, strings.Join(segments[index:], "/"))
	}
	return nil, nil
}

func appendRouteValue(values []string, value string) []string {
	result := make([]string, len(values)+1)
	copy(result, values)
	result[len(values)] = value
	return result
}

func bindRouteParams(names, values []string) routeParams {
	if len(names) != len(values) {
		return nil
	}
	parameters := make(routeParams, len(names))
	for index := range names {
		parameters[index] = routeParam{Name: names[index], Value: values[index]}
	}
	return parameters
}
