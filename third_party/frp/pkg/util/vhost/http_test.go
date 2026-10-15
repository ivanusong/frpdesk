package vhost

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	httppkg "github.com/fatedier/frp/pkg/util/http"
)

func TestHTTPServerProtocols(t *testing.T) {
	rp := NewHTTPReverseProxy(HTTPReverseProxyOptions{}, NewRouters())
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:           rp,
		ReadHeaderTimeout: time.Second,
		Protocols:         protocols,
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	defer func() {
		require.NoError(t, server.Close())
		require.ErrorIs(t, <-serveErr, http.ErrServerClosed)
	}()

	require.True(t, server.Protocols.HTTP1())
	require.True(t, server.Protocols.UnencryptedHTTP2())

	t.Run("HTTP/1.1", func(t *testing.T) {
		transport := &http.Transport{Protocols: httpProtocols(true, false)}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport}
		response, err := client.Get("http://" + listener.Addr().String() + "/")
		require.NoError(t, err)
		defer response.Body.Close()

		require.Equal(t, "HTTP/1.1", response.Proto)
		require.Equal(t, http.StatusNotFound, response.StatusCode)
	})

	t.Run("HTTP/2 prior knowledge", func(t *testing.T) {
		transport := &http.Transport{Protocols: httpProtocols(false, true)}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport}
		response, err := client.Get("http://" + listener.Addr().String() + "/")
		require.NoError(t, err)
		defer response.Body.Close()

		require.Equal(t, "HTTP/2.0", response.Proto)
		require.Equal(t, http.StatusNotFound, response.StatusCode)
	})

	t.Run("HTTP/1.1 Upgrade h2c", func(t *testing.T) {
		conn, err := net.Dial("tcp", listener.Addr().String())
		require.NoError(t, err)
		defer conn.Close()

		_, err = fmt.Fprintf(conn,
			"GET / HTTP/1.1\r\nHost: %s\r\n"+
				"Connection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\n"+
				"HTTP2-Settings: AAMAAABkAAQCAAAAAAIAAAAA\r\n\r\n",
			listener.Addr())
		require.NoError(t, err)
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		defer response.Body.Close()

		require.NotEqual(t, http.StatusSwitchingProtocols, response.StatusCode)
	})
}

func httpProtocols(http1, unencryptedHTTP2 bool) *http.Protocols {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(http1)
	protocols.SetUnencryptedHTTP2(unencryptedHTTP2)
	return protocols
}

func TestCheckRouteAuthByRequest(t *testing.T) {
	rc := &RouteConfig{
		Username: "alice",
		Password: "secret",
	}

	t.Run("accepts nil route config", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		require.True(t, checkRouteAuthByRequest(req, nil))
	})

	t.Run("accepts route without credentials", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		require.True(t, checkRouteAuthByRequest(req, &RouteConfig{}))
	})

	t.Run("accepts authorization header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetBasicAuth("alice", "secret")
		require.True(t, checkRouteAuthByRequest(req, rc))
	})

	t.Run("accepts proxy authorization header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://target.example.com/", nil)
		req.Header.Set("Proxy-Authorization", httppkg.BasicAuth("alice", "secret"))
		require.True(t, checkRouteAuthByRequest(req, rc))
	})

	t.Run("rejects authorization fallback for proxy request", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://target.example.com/", nil)
		req.SetBasicAuth("alice", "secret")
		require.False(t, checkRouteAuthByRequest(req, rc))
	})

	t.Run("rejects wrong proxy authorization even when authorization matches", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://target.example.com/", nil)
		req.SetBasicAuth("alice", "secret")
		req.Header.Set("Proxy-Authorization", httppkg.BasicAuth("alice", "wrong"))
		require.False(t, checkRouteAuthByRequest(req, rc))
	})

	t.Run("rejects when neither header matches", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://target.example.com/", nil)
		req.SetBasicAuth("alice", "wrong")
		req.Header.Set("Proxy-Authorization", httppkg.BasicAuth("alice", "wrong"))
		require.False(t, checkRouteAuthByRequest(req, rc))
	})

	t.Run("rejects proxy authorization on direct request", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Proxy-Authorization", httppkg.BasicAuth("alice", "secret"))
		require.False(t, checkRouteAuthByRequest(req, rc))
	})
}

func TestNewHTTPReverseProxyTimeoutOptions(t *testing.T) {
	tests := []struct {
		name                  string
		responseHeaderTimeout int64
		wantHeaderTimeout     time.Duration
		wantIdleTimeout       time.Duration
		wantMaxResponseHeader int64
	}{
		{
			name:                  "zero falls back to 60s",
			responseHeaderTimeout: 0,
			wantHeaderTimeout:     60 * time.Second,
			wantIdleTimeout:       60 * time.Second,
			wantMaxResponseHeader: 0,
		},
		{
			name:                  "positive keeps original behavior",
			responseHeaderTimeout: 7,
			wantHeaderTimeout:     7 * time.Second,
			wantIdleTimeout:       60 * time.Second,
			wantMaxResponseHeader: 0,
		},
		{
			name:                  "negative enables unlimited mode",
			responseHeaderTimeout: -1,
			wantHeaderTimeout:     0,
			wantIdleTimeout:       0,
			wantMaxResponseHeader: UnlimitedHeaderBytes,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := NewHTTPReverseProxy(HTTPReverseProxyOptions{ResponseHeaderTimeoutS: tt.responseHeaderTimeout}, NewRouters())
			proxy, ok := rp.proxy.(*httputil.ReverseProxy)
			require.True(t, ok)
			transport, ok := proxy.Transport.(*http.Transport)
			require.True(t, ok)
			require.Equal(t, tt.wantHeaderTimeout, transport.ResponseHeaderTimeout)
			require.Equal(t, tt.wantIdleTimeout, transport.IdleConnTimeout)
			require.Equal(t, tt.wantMaxResponseHeader, transport.MaxResponseHeaderBytes)
			// MaxIdleConnsPerHost is a connection pool setting, kept in all modes.
			require.Equal(t, 5, transport.MaxIdleConnsPerHost)
		})
	}
}

// newTimeoutTestProxy starts a local backend server and an frp vhost reverse
// proxy routing host "example.com" to it, then returns the proxy URL.
func newTimeoutTestProxy(t *testing.T, opts HTTPReverseProxyOptions) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/slow-header", func(w http.ResponseWriter, r *http.Request) {
		delay, err := time.ParseDuration(r.URL.Query().Get("delay"))
		if err != nil || delay < 0 {
			delay = time.Second
		}
		time.Sleep(delay)
		w.Header().Set("X-Backend", "slow")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		for i := 0; i < 3; i++ {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
			flusher.Flush()
			time.Sleep(300 * time.Millisecond)
		}
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, "bytes=%d", n)
	})
	backend := httptest.NewServer(mux)
	t.Cleanup(backend.Close)

	rp := NewHTTPReverseProxy(opts, NewRouters())
	require.NoError(t, rp.Register(RouteConfig{
		Domain: "example.com",
		CreateConnFn: func(remoteAddr string) (net.Conn, error) {
			return net.Dial("tcp", backend.Listener.Addr().String())
		},
	}))
	front := httptest.NewServer(rp)
	t.Cleanup(front.Close)
	return front.URL
}

func doVhostRequest(t *testing.T, baseURL, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	require.NoError(t, err)
	req.Host = "example.com"
	resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
	require.NoError(t, err)
	return resp
}

func TestHTTPReverseProxyTimeoutModes(t *testing.T) {
	t.Run("default mode handles short-delay backend", func(t *testing.T) {
		frontURL := newTimeoutTestProxy(t, HTTPReverseProxyOptions{})
		resp := doVhostRequest(t, frontURL, "/slow-header?delay=1s")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "slow", resp.Header.Get("X-Backend"))
	})

	t.Run("positive timeout aborts slow response header", func(t *testing.T) {
		frontURL := newTimeoutTestProxy(t, HTTPReverseProxyOptions{ResponseHeaderTimeoutS: 1})
		resp := doVhostRequest(t, frontURL, "/slow-header?delay=3s")
		defer resp.Body.Close()
		require.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
	})

	t.Run("unlimited mode waits for slow response header", func(t *testing.T) {
		frontURL := newTimeoutTestProxy(t, HTTPReverseProxyOptions{ResponseHeaderTimeoutS: -1})
		resp := doVhostRequest(t, frontURL, "/slow-header?delay=3s")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "slow", resp.Header.Get("X-Backend"))
	})

	t.Run("unlimited mode streams request body", func(t *testing.T) {
		frontURL := newTimeoutTestProxy(t, HTTPReverseProxyOptions{ResponseHeaderTimeoutS: -1})
		body := bytes.Repeat([]byte("a"), 4<<20)
		req, err := http.NewRequest(http.MethodPost, frontURL+"/upload", bytes.NewReader(body))
		require.NoError(t, err)
		req.Host = "example.com"
		resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("bytes=%d", len(body)), string(got))
	})
}

func TestHTTPReverseProxyUnlimitedModeSSE(t *testing.T) {
	frontURL := newTimeoutTestProxy(t, HTTPReverseProxyOptions{ResponseHeaderTimeoutS: -1})
	resp := doVhostRequest(t, frontURL, "/sse")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	scanner := bufio.NewScanner(resp.Body)
	var events []string
	var firstAt, lastAt time.Time
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			if firstAt.IsZero() {
				firstAt = time.Now()
			}
			lastAt = time.Now()
			events = append(events, strings.TrimPrefix(line, "data: "))
		}
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, []string{"0", "1", "2"}, events)
	// events arrive over time instead of being buffered until the backend closes
	require.Greater(t, lastAt.Sub(firstAt), 300*time.Millisecond)
}

func TestHTTPReverseProxyUnlimitedModeSlowResponseHeader65s(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 65s real-wait test in short mode")
	}
	frontURL := newTimeoutTestProxy(t, HTTPReverseProxyOptions{ResponseHeaderTimeoutS: -1})
	resp := doVhostRequest(t, frontURL, "/slow-header?delay=65s")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "done", string(body))
}

func TestGetRequestRouteUser(t *testing.T) {
	t.Run("proxy request uses proxy authorization username", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://target.example.com/", nil)
		req.Host = "target.example.com"
		req.Header.Set("Proxy-Authorization", httppkg.BasicAuth("proxy-user", "proxy-pass"))
		req.SetBasicAuth("direct-user", "direct-pass")

		require.Equal(t, "proxy-user", getRequestRouteUser(req))
	})

	t.Run("connect request keeps proxy authorization routing", func(t *testing.T) {
		req := httptest.NewRequest("CONNECT", "http://target.example.com:443", nil)
		req.Host = "target.example.com:443"
		req.Header.Set("Proxy-Authorization", httppkg.BasicAuth("proxy-user", "proxy-pass"))
		req.SetBasicAuth("direct-user", "direct-pass")

		require.Equal(t, "proxy-user", getRequestRouteUser(req))
	})

	t.Run("direct request uses authorization username", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = "example.com"
		req.SetBasicAuth("direct-user", "direct-pass")

		require.Equal(t, "direct-user", getRequestRouteUser(req))
	})

	t.Run("proxy request does not fall back when proxy authorization is invalid", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://target.example.com/", nil)
		req.Host = "target.example.com"
		req.Header.Set("Proxy-Authorization", "Basic !!!")
		req.SetBasicAuth("direct-user", "direct-pass")

		require.Empty(t, getRequestRouteUser(req))
	})
}
