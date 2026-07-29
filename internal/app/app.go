package app

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Felipyan19/webhook-inspector-go/internal/store"
)

const maxBodySize = 1024 * 1024

//go:embed web/*
var webFiles embed.FS

type App struct {
	store   *store.Store
	mux     *http.ServeMux
	client  *http.Client
	streams *broker
}

func New(db *store.Store) http.Handler {
	a := &App{
		store:   db,
		mux:     http.NewServeMux(),
		client:  &http.Client{Timeout: 10 * time.Second},
		streams: newBroker(),
	}
	a.routes()
	return securityHeaders(a.mux)
}

func (a *App) routes() {
	assets, _ := fs.Sub(webFiles, "web")
	a.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	a.mux.HandleFunc("GET /{$}", a.index)
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	a.mux.HandleFunc("POST /api/endpoints", a.createEndpoint)
	a.mux.HandleFunc("GET /api/endpoints", a.listEndpoints)
	a.mux.HandleFunc("GET /api/endpoints/{token}/events", a.listEvents)
	a.mux.HandleFunc("GET /api/endpoints/{token}/stream", a.stream)
	a.mux.HandleFunc("POST /api/events/{id}/replay", a.replay)
	a.mux.HandleFunc("/hooks/{token}", a.capture)
}

func (a *App) index(w http.ResponseWriter, _ *http.Request) {
	data, _ := webFiles.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (a *App) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		writeError(w, http.StatusBadRequest, "name must contain between 1 and 80 characters")
		return
	}
	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate endpoint")
		return
	}
	endpoint, err := a.store.CreateEndpoint(r.Context(), input.Name, token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create endpoint")
		return
	}
	writeJSON(w, http.StatusCreated, endpoint)
}

func (a *App) listEndpoints(w http.ResponseWriter, r *http.Request) {
	endpoints, err := a.store.ListEndpoints(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list endpoints")
		return
	}
	writeJSON(w, http.StatusOK, endpoints)
}

func (a *App) listEvents(w http.ResponseWriter, r *http.Request) {
	endpoint, err := a.store.EndpointByToken(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	events, err := a.store.ListEvents(r.Context(), endpoint.ID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list events")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (a *App) capture(w http.ResponseWriter, r *http.Request) {
	endpoint, err := a.store.EndpointByToken(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1 MB")
		return
	}
	event, err := a.store.SaveEvent(r.Context(), store.Event{
		EndpointID: endpoint.ID,
		Method:     r.Method,
		Path:       r.URL.Path,
		Query:      r.URL.RawQuery,
		Headers:    r.Header.Clone(),
		Body:       string(body),
		RemoteAddr: r.RemoteAddr,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save webhook")
		return
	}
	a.streams.publish(endpoint.Token, event)
	writeJSON(w, http.StatusAccepted, map[string]any{"received": true, "eventId": event.ID})
}

func (a *App) stream(w http.ResponseWriter, r *http.Request) {
	if _, err := a.store.EndpointByToken(r.Context(), r.PathValue("token")); err != nil {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, cancel := a.streams.subscribe(r.PathValue("token"))
	defer cancel()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: webhook\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (a *App) replay(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event id")
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	target, err := safeTarget(input.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	event, err := a.store.Event(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), event.Method, target.String(), bytes.NewBufferString(event.Body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not create replay request")
		return
	}
	for key, values := range event.Headers {
		if isHopByHop(key) || strings.EqualFold(key, "Host") {
			continue
		}
		request.Header[key] = values
	}
	response, err := a.client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "target could not be reached")
		return
	}
	defer response.Body.Close()
	preview, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	writeJSON(w, http.StatusOK, map[string]any{
		"status": response.StatusCode,
		"body":   string(preview),
	})
}

func randomToken() (string, error) {
	data := make([]byte, 10)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func safeTarget(raw string) (*url.URL, error) {
	target, err := url.ParseRequestURI(raw)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, errors.New("a valid http or https URL is required")
	}
	host := target.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil, errors.New("local network targets are not allowed")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return nil, errors.New("local network targets are not allowed")
	}
	return target, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self'; script-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func isHopByHop(header string) bool {
	switch http.CanonicalHeaderKey(header) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length":
		return true
	default:
		return false
	}
}

type broker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan store.Event]struct{}
}

func newBroker() *broker {
	return &broker{subscribers: make(map[string]map[chan store.Event]struct{})}
}

func (b *broker) subscribe(token string) (<-chan store.Event, func()) {
	ch := make(chan store.Event, 8)
	b.mu.Lock()
	if b.subscribers[token] == nil {
		b.subscribers[token] = make(map[chan store.Event]struct{})
	}
	b.subscribers[token][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subscribers[token], ch)
		b.mu.Unlock()
	}
}

func (b *broker) publish(token string, event store.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers[token] {
		select {
		case ch <- event:
		default:
		}
	}
}
