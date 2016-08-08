package dev

import (
	"encoding/json"
	"net"
	"net/http"
	"puma/httpu"
	"puma/httputil"
	"strings"
	"time"

	"github.com/bmizerany/pat"
)

type HTTPServer struct {
	Address    string
	TLSAddress string
	Pool       *AppPool
	Debug      bool

	mux       *pat.PatternServeMux
	transport *httpu.Transport
	proxy     *httputil.ReverseProxy
}

func (h *HTTPServer) Setup() {
	h.transport = &httpu.Transport{
		Dial: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 10 * time.Second,
		}).Dial,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	h.Pool.AppClosed = h.AppClosed

	h.proxy = &httputil.ReverseProxy{
		Director:      h.director,
		Transport:     h.transport,
		FlushInterval: 1 * time.Second,
		Debug:         h.Debug,
	}

	h.mux = pat.New()

	h.mux.Get("/status", http.HandlerFunc(h.status))
}

func (h *HTTPServer) AppClosed(app *App) {
	// Whenever an app is closed, wipe out all idle conns. This
	// obviously closes down more than just this one apps connections
	// but that's ok.
	h.transport.CloseIdleConnections()
}

func pruneSub(name string) string {
	dot := strings.IndexByte(name, '.')
	if dot == -1 {
		return ""
	}

	return name[dot+1:]
}

func (h *HTTPServer) hostForApp(name string) (string, string, error) {
	var (
		app *App
		err error
	)

	for name != "" {
		app, err = h.Pool.App(name)
		if err != nil {
			if err == ErrUnknownApp {
				name = pruneSub(name)
				continue
			}

			return "", "", err
		}

		break
	}

	if app == nil {
		app, err = h.Pool.App("default")
		if err != nil {
			return "", "", err
		}
	}

	err = app.WaitTilReady()
	if err != nil {
		return "", "", err
	}

	return app.Scheme, app.Address(), nil
}

func (h *HTTPServer) director(req *http.Request) error {
	dot := strings.LastIndexByte(req.Host, '.')

	var name string
	if dot == -1 {
		name = req.Host
	} else {
		name = req.Host[:dot]
	}

	var err error
	req.URL.Scheme, req.URL.Host, err = h.hostForApp(name)
	return err
}

func (h *HTTPServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Host == "puma-dev" {
		h.mux.ServeHTTP(w, req)
	} else {
		h.proxy.ServeHTTP(w, req)
	}
}

func (h *HTTPServer) status(w http.ResponseWriter, req *http.Request) {
	type appStatus struct {
		Scheme  string `json:"scheme"`
		Address string `json:"address"`
		Status  string `json:"status"`
		Log     string `json:"log"`
	}

	statuses := map[string]appStatus{}

	h.Pool.ForApps(func(a *App) {
		var status string

		switch a.Status() {
		case Dead:
			status = "dead"
		case Booting:
			status = "booting"
		case Running:
			status = "running"
		default:
			status = "unknown"
		}

		statuses[a.Name] = appStatus{
			Scheme:  a.Scheme,
			Address: a.Address(),
			Status:  status,
			Log:     a.Log(),
		}
	})

	json.NewEncoder(w).Encode(statuses)
}
