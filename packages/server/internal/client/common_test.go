package client

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/codetrek/haystack/server/internal/conf"
	"github.com/codetrek/haystack/server/internal/shared/types"
)

// startMockServer creates a test HTTP server and configures conf to point to it.
// Returns the server (caller must defer server.Close()) and restores conf on cleanup.
func startMockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)

	// Parse the port from the test server URL
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("failed to parse test server port: %v", err)
	}

	var portInt int
	fmt.Sscanf(port, "%d", &portInt)

	oldPort := conf.Get().Global.Port
	oldSocket := conf.Get().Global.SocketPath
	conf.Get().Global.Port = portInt
	conf.Get().Global.SocketPath = ""

	t.Cleanup(func() {
		server.Close()
		conf.Get().Global.Port = oldPort
		conf.Get().Global.SocketPath = oldSocket
	})

	return server
}

// startUnixMockServer creates a test HTTP server listening on a Unix socket and
// configures conf to point to it.
func startUnixMockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	socketPath := fmt.Sprintf("%s/haystack_test_%d.sock", os.TempDir(), os.Getpid())

	// Remove socket file if it already exists
	os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on unix socket: %v", err)
	}

	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()

	oldPort := conf.Get().Global.Port
	oldSocket := conf.Get().Global.SocketPath
	conf.Get().Global.SocketPath = socketPath
	conf.Get().Global.Port = 0

	t.Cleanup(func() {
		server.Close()
		os.Remove(socketPath)
		conf.Get().Global.Port = oldPort
		conf.Get().Global.SocketPath = oldSocket
	})

	return server
}

func makeCommonResponse(t *testing.T, code int, message string, data interface{}) []byte {
	t.Helper()
	var rawData *json.RawMessage
	if data != nil {
		d, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("json.Marshal data: %v", err)
		}
		raw := json.RawMessage(d)
		rawData = &raw
	}
	resp := types.CommonResponse{
		Code:    code,
		Message: message,
		Data:    rawData,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal CommonResponse: %v", err)
	}
	return b
}

func TestServerRequest_TCP_Success(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/test/endpoint" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", map[string]string{"key": "value"}))
	})

	result, err := serverRequest("/test/endpoint", []byte(`{"test":"data"}`))
	if err != nil {
		t.Fatalf("serverRequest error: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", result.StatusCode)
	}
	if result.Body.Message != "ok" {
		t.Errorf("expected message 'ok', got %q", result.Body.Message)
	}
}

func TestServerRequest_UnixSocket_Success(t *testing.T) {
	startUnixMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/test/unix" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "unix-ok", map[string]string{"result": "pass"}))
	})

	result, err := serverRequest("/test/unix", []byte(`{}`))
	if err != nil {
		t.Fatalf("serverRequest error: %v", err)
	}
	if result.Body.Message != "unix-ok" {
		t.Errorf("expected message 'unix-ok', got %q", result.Body.Message)
	}
}

func TestServerRequest_ErrorCode(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "something went wrong", nil))
	})

	_, err := serverRequest("/test/error-code", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for non-zero response code")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("error should contain message, got: %v", err)
	}
}

func TestServerRequest_ConnectionRefused(t *testing.T) {
	// Point to a port that nothing is listening on
	oldPort := conf.Get().Global.Port
	oldSocket := conf.Get().Global.SocketPath
	conf.Get().Global.Port = 1 // port 1 typically has nothing
	conf.Get().Global.SocketPath = ""
	defer func() {
		conf.Get().Global.Port = oldPort
		conf.Get().Global.SocketPath = oldSocket
	}()

	_, err := serverRequest("/test/nothing", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error when server is not running")
	}
	if !strings.Contains(err.Error(), "failed to connect") {
		t.Errorf("expected 'failed to connect' error, got: %v", err)
	}
}

func TestServerRequest_InvalidJSON(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not valid json"))
	})

	_, err := serverRequest("/test/badjson", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "failed to unmarshal") {
		t.Errorf("expected unmarshal error, got: %v", err)
	}
}
