package models

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type modelScopeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelScopeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func modelResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestListFreeTierProviderUsesModelScopeInternationalSite(t *testing.T) {
	var hosts []string
	client := &http.Client{Transport: modelScopeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		return modelResponse(req, http.StatusOK, `{"data":[{"id":"ZhipuAI/GLM-5.2"}]}`), nil
	})}

	list, err := ListFreeTierProvider(client, "modelscope", "ms-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0] != "api-inference.modelscope.ai" {
		t.Fatalf("hosts = %v, want international endpoint only", hosts)
	}
	if len(list) != 1 || list[0].Base != ModelScopeBase {
		t.Fatalf("models = %+v", list)
	}
}

func TestListFreeTierProviderFallsBackToModelScopeChina(t *testing.T) {
	var hosts []string
	client := &http.Client{Transport: modelScopeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		if req.URL.Host == "api-inference.modelscope.ai" {
			return modelResponse(req, http.StatusUnauthorized, `{"error":"wrong site"}`), nil
		}
		return modelResponse(req, http.StatusOK, `{"data":[{"id":"Qwen/Qwen3"}]}`), nil
	})}

	list, err := ListFreeTierProvider(client, "modelscope", "ms-cn-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[1] != "api-inference.modelscope.cn" {
		t.Fatalf("hosts = %v, want international then China", hosts)
	}
	if len(list) != 1 || list[0].Base != ModelScopeCNBase {
		t.Fatalf("models = %+v", list)
	}
}
