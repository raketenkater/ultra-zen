package models

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeModelsServer serves a /models list backing the main Zen gateway, letting
// tests control whether a model reappears without real network access.
func fakeModelsServer(ids ...string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[`))
		for i, id := range ids {
			if i > 0 {
				w.Write([]byte(","))
			}
			w.Write([]byte(`{"id":"` + id + `"}`))
		}
		w.Write([]byte(`]}`))
	}))
	return srv
}

func TestRecheckClearsDeniedModelWhenServedAgain(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := MarkUnavailable("opencode-go", "deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	// The model is denied initially.
	if !IsUnavailable("opencode-go", "deepseek-v4-flash") {
		t.Fatal("precondition: model should be denied")
	}

	// Now the go-tier catalog serves it again; re-check once against the fake
	// base. BaseForProvider maps opencode-go → GoBase, but this test drives
	// recheckProvider directly with the fake URL so the const stays untouched.
	srv := fakeModelsServer("glm-5.2", "deepseek-v4-flash", "kimi-k3")
	defer srv.Close()

	recheckProvider(&http.Client{Timeout: 5 * time.Second}, srv.URL, "test-key", "opencode-go", []string{"deepseek-v4-flash"})

	if IsUnavailable("opencode-go", "deepseek-v4-flash") {
		t.Fatal("denial should be cleared once the model is served again")
	}
}

func TestRecheckClearsOnlyModelFromSiblingDenial(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := MarkUnavailable("opencode-go", "deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	if err := MarkUnavailable("opencode-go", "laguna-s-2.1-free"); err != nil {
		t.Fatal(err)
	}

	srv := fakeModelsServer("deepseek-v4-flash") // only flash returns
	defer srv.Close()

	recheckProvider(&http.Client{Timeout: 5 * time.Second}, srv.URL, "test", "opencode-go", []string{"deepseek-v4-flash", "laguna-s-2.1-free"})

	if IsUnavailable("opencode-go", "deepseek-v4-flash") {
		t.Fatal("flash came back and should be cleared")
	}
	if !IsUnavailable("opencode-go", "laguna-s-2.1-free") {
		t.Fatal("laguna still absent and must stay denied")
	}
}

func TestRecheckKeepsDenialWhenModelStillAbsent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := MarkUnavailable("opencode-go", "deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}

	srv := fakeModelsServer("glm-5.2") // flash still missing
	defer srv.Close()

	recheckProvider(&http.Client{Timeout: 5 * time.Second}, srv.URL, "test", "opencode-go", []string{"deepseek-v4-flash"})

	if !IsUnavailable("opencode-go", "deepseek-v4-flash") {
		t.Fatal("model still missing from catalog; denial must persist")
	}
}