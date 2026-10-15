// Copyright 2017 fatedier, fatedier@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vhost

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	stdlog "log"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	libio "github.com/fatedier/golib/io"
	"github.com/fatedier/golib/pool"

	httppkg "github.com/fatedier/frp/pkg/util/http"
	"github.com/fatedier/frp/pkg/util/log"
)

var ErrNoRouteFound = errors.New("no route found")

// UnlimitedHeaderBytes is a safe near-maximum integer cap for HTTP header
// limits (MaxInt-4096). net/http requires integer caps, so unlimited mode
// does not remove limits; it raises them to this value. The 4096 headroom is
// needed because http.Server adds a 4096-byte bufio slop on top of
// MaxHeaderBytes (initialReadLimitSize) and would otherwise overflow int64.
const UnlimitedHeaderBytes = math.MaxInt - 4096

type HTTPReverseProxyOptions struct {
	ResponseHeaderTimeoutS int64
}

type HTTPReverseProxy struct {
	proxy       http.Handler
	vhostRouter *Routers

	responseHeaderTimeout time.Duration
}

// NewHTTPReverseProxy creates a reverse proxy for the vhost HTTP gateway.
//
// option.ResponseHeaderTimeoutS semantics (mirrors server config
// vhostHTTPTimeout):
//   - > 0: use the value as the response header timeout in seconds.
//   - == 0: fall back to the legacy default of 60 seconds.
//   - < 0: unlimited mode. The response header timeout is disabled (set to
//     0), the transport's response header size cap is raised to
//     UnlimitedHeaderBytes (a safe near-max integer bound), and idle backend
//     connections are kept, so slow backends, uploads, downloads and SSE
//     streams are never aborted by the gateway itself.
func NewHTTPReverseProxy(option HTTPReverseProxyOptions, vhostRouter *Routers) *HTTPReverseProxy {
	unlimited := option.ResponseHeaderTimeoutS < 0
	if option.ResponseHeaderTimeoutS == 0 {
		option.ResponseHeaderTimeoutS = 60
	}
	responseHeaderTimeout := time.Duration(option.ResponseHeaderTimeoutS) * time.Second
	if unlimited {
		responseHeaderTimeout = 0
	}
	rp := &HTTPReverseProxy{
		responseHeaderTimeout: responseHeaderTimeout,
		vhostRouter:           vhostRouter,
	}
	proxy := &httputil.ReverseProxy{
		// Modify incoming requests by route policies.
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header["X-Forwarded-For"] = r.In.Header["X-Forwarded-For"]
			r.SetXForwarded()
			req := r.Out
			req.URL.Scheme = "http"
			reqRouteInfo := req.Context().Value(RouteInfoKey).(*RequestRouteInfo)
			originalHost, _ := httppkg.CanonicalHost(reqRouteInfo.Host)

			rc := req.Context().Value(RouteConfigKey).(*RouteConfig)
			if rc != nil {
				if rc.RewriteHost != "" {
					req.Host = rc.RewriteHost
				}

				var endpoint string
				if rc.ChooseEndpointFn != nil {
					// ignore error here, it will use CreateConnFn instead later
					endpoint, _ = rc.ChooseEndpointFn()
					reqRouteInfo.Endpoint = endpoint
					log.Tracef("choose endpoint name [%s] for http request host [%s] path [%s] httpuser [%s]",
						endpoint, originalHost, reqRouteInfo.URL, reqRouteInfo.HTTPUser)
				}
				// Set {domain}.{location}.{routeByHTTPUser}.{endpoint} as URL host here to let http transport reuse connections.
				req.URL.Host = rc.Domain + "." +
					base64.StdEncoding.EncodeToString([]byte(rc.Location)) + "." +
					base64.StdEncoding.EncodeToString([]byte(rc.RouteByHTTPUser)) + "." +
					base64.StdEncoding.EncodeToString([]byte(endpoint))

				for k, v := range rc.Headers {
					req.Header.Set(k, v)
				}
			} else {
				req.URL.Host = req.Host
			}
		},
		ModifyResponse: func(r *http.Response) error {
			rc := r.Request.Context().Value(RouteConfigKey).(*RouteConfig)
			if rc != nil {
				for k, v := range rc.ResponseHeaders {
					r.Header.Set(k, v)
				}
			}
			return nil
		},
		// Create a connection to one proxy routed by route policy.
		Transport: newVhostTransport(rp, unlimited),
		BufferPool: pool.NewBuffer(32 * 1024),
		ErrorLog:   stdlog.New(log.NewWriteLogger(log.WarnLevel, 2), "", 0),
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			log.Logf(log.WarnLevel, 1, "do http proxy request [host: %s] error: %v", req.Host, err)
			if err != nil {
				if e, ok := err.(net.Error); ok && e.Timeout() {
					rw.WriteHeader(http.StatusGatewayTimeout)
					return
				}
			}
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write(getNotFoundPageContent())
		},
	}
	rp.proxy = proxy
	return rp
}

// newVhostTransport builds the transport used by the vhost reverse proxy.
// In unlimited mode (vhostHTTPTimeout < 0) the response header timeout and
// idle connection timeout are disabled and the default 10MB response header
// cap is raised to UnlimitedHeaderBytes (safe near-max integer bound).
// MaxIdleConnsPerHost is kept as a connection pool setting, not a business
// rate limit.
func newVhostTransport(rp *HTTPReverseProxy, unlimited bool) *http.Transport {
	transport := &http.Transport{
		ResponseHeaderTimeout: rp.responseHeaderTimeout,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConnsPerHost:   5,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return rp.CreateConnection(ctx.Value(RouteInfoKey).(*RequestRouteInfo), true)
		},
		Proxy: func(req *http.Request) (*url.URL, error) {
			// Use proxy mode if there is host in HTTP first request line.
			// GET http://example.com/ HTTP/1.1
			// Host: example.com
			//
			// Normal:
			// GET / HTTP/1.1
			// Host: example.com
			urlHost := req.Context().Value(RouteInfoKey).(*RequestRouteInfo).URLHost
			if urlHost != "" {
				return req.URL, nil
			}
			return nil, nil
		},
	}
	if unlimited {
		transport.IdleConnTimeout = 0
		transport.MaxResponseHeaderBytes = UnlimitedHeaderBytes
	}
	return transport
}

// Register register the route config to reverse proxy
// reverse proxy will use CreateConnFn from routeCfg to create a connection to the remote service
func (rp *HTTPReverseProxy) Register(routeCfg RouteConfig) error {
	err := rp.vhostRouter.Add(routeCfg.Domain, routeCfg.Location, routeCfg.RouteByHTTPUser, &routeCfg)
	if err != nil {
		return err
	}
	return nil
}

// UnRegister unregister route config by domain and location
func (rp *HTTPReverseProxy) UnRegister(routeCfg RouteConfig) {
	rp.vhostRouter.Del(routeCfg.Domain, routeCfg.Location, routeCfg.RouteByHTTPUser)
}

func (rp *HTTPReverseProxy) GetRouteConfig(domain, location, routeByHTTPUser string) *RouteConfig {
	vr, ok := rp.vhostRouter.getByRoute(domain, location, routeByHTTPUser)
	if ok {
		log.Debugf("get new http request host [%s] path [%s] httpuser [%s]", domain, location, routeByHTTPUser)
		return vr.payload.(*RouteConfig)
	}
	return nil
}

// CreateConnection create a new connection by route config
func (rp *HTTPReverseProxy) CreateConnection(reqRouteInfo *RequestRouteInfo, byEndpoint bool) (net.Conn, error) {
	host, _ := httppkg.CanonicalHost(reqRouteInfo.Host)
	vr, ok := rp.vhostRouter.getByRoute(host, reqRouteInfo.URL, reqRouteInfo.HTTPUser)
	if ok {
		if byEndpoint {
			fn := vr.payload.(*RouteConfig).CreateConnByEndpointFn
			if fn != nil {
				return fn(reqRouteInfo.Endpoint, reqRouteInfo.RemoteAddr)
			}
		}
		fn := vr.payload.(*RouteConfig).CreateConnFn
		if fn != nil {
			return fn(reqRouteInfo.RemoteAddr)
		}
	}
	return nil, fmt.Errorf("%v: %s %s %s", ErrNoRouteFound, host, reqRouteInfo.URL, reqRouteInfo.HTTPUser)
}

func checkRouteAuthByRequest(req *http.Request, rc *RouteConfig) bool {
	if rc == nil {
		return true
	}
	if rc.Username == "" && rc.Password == "" {
		return true
	}

	if req.URL.Host != "" {
		proxyAuth := req.Header.Get("Proxy-Authorization")
		if proxyAuth == "" {
			return false
		}
		user, passwd, ok := httppkg.ParseBasicAuth(proxyAuth)
		return ok && user == rc.Username && passwd == rc.Password
	}

	user, passwd, ok := req.BasicAuth()
	return ok && user == rc.Username && passwd == rc.Password
}

func (rp *HTTPReverseProxy) connectHandler(rw http.ResponseWriter, req *http.Request) {
	hj, ok := rw.(http.Hijacker)
	if !ok {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	client, _, err := hj.Hijack()
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	remote, err := rp.CreateConnection(req.Context().Value(RouteInfoKey).(*RequestRouteInfo), false)
	if err != nil {
		_ = NotFoundResponse().Write(client)
		client.Close()
		return
	}
	_ = req.Write(remote)
	go libio.Join(remote, client)
}

func getRequestRouteUser(req *http.Request) string {
	if req.URL.Host != "" {
		proxyAuth := req.Header.Get("Proxy-Authorization")
		if proxyAuth == "" {
			// Preserve legacy proxy-mode routing when clients send only Authorization,
			// so requests still hit the matched route and return 407 instead of 404.
			// Auth validation intentionally does not share this fallback.
			user, _, _ := req.BasicAuth()
			return user
		}
		user, _, _ := httppkg.ParseBasicAuth(proxyAuth)
		return user
	}
	user, _, _ := req.BasicAuth()
	return user
}

func (rp *HTTPReverseProxy) injectRequestInfoToCtx(req *http.Request) *http.Request {
	user := getRequestRouteUser(req)

	reqRouteInfo := &RequestRouteInfo{
		URL:        req.URL.Path,
		Host:       req.Host,
		HTTPUser:   user,
		RemoteAddr: req.RemoteAddr,
		URLHost:    req.URL.Host,
	}

	originalHost, _ := httppkg.CanonicalHost(reqRouteInfo.Host)
	rc := rp.GetRouteConfig(originalHost, reqRouteInfo.URL, reqRouteInfo.HTTPUser)

	newctx := req.Context()
	newctx = context.WithValue(newctx, RouteInfoKey, reqRouteInfo)
	newctx = context.WithValue(newctx, RouteConfigKey, rc)
	return req.Clone(newctx)
}

func (rp *HTTPReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	newreq := rp.injectRequestInfoToCtx(req)
	rc := newreq.Context().Value(RouteConfigKey).(*RouteConfig)
	if !checkRouteAuthByRequest(req, rc) {
		if req.URL.Host != "" {
			rw.Header().Set("Proxy-Authenticate", `Basic realm="Restricted"`)
			http.Error(rw, http.StatusText(http.StatusProxyAuthRequired), http.StatusProxyAuthRequired)
		} else {
			rw.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(rw, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		}
		return
	}

	if req.Method == http.MethodConnect {
		rp.connectHandler(rw, newreq)
	} else {
		rp.proxy.ServeHTTP(rw, newreq)
	}
}
