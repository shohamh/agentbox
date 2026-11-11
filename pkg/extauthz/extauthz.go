// Package extauthz has helpers for Envoy's ext_authz gRPC protocol, used by
// gatekeeper (front gateway) and agentbox-manager (local Envoy).
package extauthz

import (
	"encoding/json"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
)

func hdr(k, v string) *corev3.HeaderValueOption {
	return &corev3.HeaderValueOption{
		Header:       &corev3.HeaderValue{Key: k, Value: v},
		AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
	}
}

// Allow lets the request through, setting and removing upstream headers.
func Allow(set map[string]string, remove ...string) *authv3.CheckResponse {
	var hs []*corev3.HeaderValueOption
	for k, v := range set {
		hs = append(hs, hdr(k, v))
	}
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.OK)},
		HttpResponse: &authv3.CheckResponse_OkResponse{OkResponse: &authv3.OkHttpResponse{
			Headers: hs, HeadersToRemove: remove,
		}},
	}
}

// Deny answers the client directly with an OpenAI-style error body, so
// Codex and opencode show the message instead of hanging or retrying blindly.
func Deny(code int, errType, msg string) *authv3.CheckResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"message": msg, "type": errType, "code": errType,
	}})
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.PermissionDenied), Message: msg},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{DeniedResponse: &authv3.DeniedHttpResponse{
			Status:  &typev3.HttpStatus{Code: typev3.StatusCode(code)},
			Body:    string(body),
			Headers: []*corev3.HeaderValueOption{hdr("content-type", "application/json"), hdr("x-agentbox-denied", errType)},
		}},
	}
}
