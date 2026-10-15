package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/auth"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"golang.org/x/oauth2"
)

type config struct {
	address, name, mode, tokenFile, httpAddress, apiToken string
	retry                                                 time.Duration
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func loadConfig() (config, error) {
	c := config{address: env("MC_SERVER_ADDRESS", ""), name: env("MC_BOT_NAME", "GoBot"), mode: env("MC_AUTH_MODE", "microsoft"), tokenFile: env("MC_TOKEN_FILE", "/data/token.json"), httpAddress: env("HTTP_ADDRESS", ":8080"), apiToken: os.Getenv("API_TOKEN")}
	var err error
	if _, _, err = net.SplitHostPort(c.address); err != nil {
		return c, fmt.Errorf("MC_SERVER_ADDRESS must be host:port: %w", err)
	}
	if c.mode != "microsoft" && c.mode != "offline" {
		return c, errors.New("MC_AUTH_MODE must be microsoft or offline")
	}
	if len(c.apiToken) < 24 || c.apiToken == "replace-with-a-long-random-secret" {
		return c, errors.New("set API_TOKEN to a random secret of at least 24 characters")
	}
	c.retry, err = time.ParseDuration(env("RECONNECT_DELAY", "10s"))
	if err != nil || c.retry < time.Second {
		return c, errors.New("RECONNECT_DELAY must be at least 1s")
	}
	return c, nil
}

// persistentSource saves refreshed credentials atomically, never logging tokens.
type persistentSource struct {
	source oauth2.TokenSource
	path   string
}

func (s *persistentSource) Token() (*oauth2.Token, error) {
	t, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return nil, err
	}
	if err = os.WriteFile(s.path+".tmp", b, 0600); err != nil {
		return nil, err
	}
	if err = os.Rename(s.path+".tmp", s.path); err != nil {
		return nil, err
	}
	return t, nil
}
func tokenSource(ctx context.Context, path string) (oauth2.TokenSource, error) {
	var t oauth2.Token
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("invalid token file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if t.RefreshToken == "" {
		tok, err := auth.RequestLiveTokenContext(ctx, os.Stdout)
		if err != nil {
			return nil, err
		}
		t = *tok
	}
	return &persistentSource{source: auth.AndroidConfig.TokenSource(ctx, &t), path: path}, nil
}

type bot struct {
	mu          sync.Mutex
	conn        *minecraft.Conn
	connectedAt time.Time
	lastError   string
}

func (b *bot) send(message string) error {
	if strings.TrimSpace(message) == "" || !utf8.ValidString(message) || len(message) > 512 || strings.ContainsAny(message, "\r\n") {
		return errors.New("message must be a single line of 1–512 UTF-8 bytes")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return errors.New("bot is disconnected")
	}
	id := b.conn.IdentityData()
	return b.conn.WritePacket(&packet.Text{TextType: packet.TextTypeChat, SourceName: id.DisplayName, XUID: id.XUID, Message: message})
}
func (b *bot) session(ctx context.Context, c config, source oauth2.TokenSource) error {
	dialer := minecraft.Dialer{TokenSource: source, IdentityData: login.IdentityData{DisplayName: c.name}}
	dialCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(dialCtx, "raknet", c.address)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.DoSpawnTimeout(45 * time.Second); err != nil {
		return err
	}
	b.mu.Lock()
	b.conn = conn
	b.connectedAt = time.Now()
	b.lastError = ""
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.conn = nil; b.mu.Unlock() }()
	slog.Info("bot joined", "server", c.address, "name", conn.IdentityData().DisplayName)
	lastReply := time.Time{}
	for {
		pk, err := conn.ReadPacket()
		if err != nil {
			return err
		}
		p, ok := pk.(*packet.Text)
		if !ok || p.TextType != packet.TextTypeChat || p.SourceName == conn.IdentityData().DisplayName {
			continue
		}
		if time.Since(lastReply) < 2*time.Second {
			continue
		}
		var reply string
		switch strings.TrimSpace(p.Message) {
		case "!ping":
			reply = "pong"
		case "!help":
			reply = "GoBot: !ping, !help"
		}
		if reply != "" {
			lastReply = time.Now()
			if err := b.send(reply); err != nil {
				return err
			}
		}
	}
}
func (b *bot) run(ctx context.Context, c config) {
	var source oauth2.TokenSource
	for ctx.Err() == nil {
		var err error
		if c.mode == "microsoft" && source == nil {
			source, err = tokenSource(ctx, c.tokenFile)
		}
		if err == nil {
			err = b.session(ctx, c, source)
		}
		if ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		b.lastError = err.Error()
		b.mu.Unlock()
		slog.Warn("connection failed; retrying", "error", err, "delay", c.retry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.retry):
		}
	}
}
func (b *bot) handler(secret string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		ready := b.conn != nil
		b.mu.Unlock()
		if !ready {
			http.Error(w, "disconnected", 503)
			return
		}
		w.WriteHeader(200)
	})
	protected := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api/status", protected(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"connected": b.conn != nil, "connected_at": b.connectedAt, "last_error": b.lastError})
	}))
	mux.HandleFunc("POST /api/chat", protected(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2048)
		var body struct {
			Message string `json:"message"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&body); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		if err := d.Decode(new(any)); err != io.EOF {
			http.Error(w, "invalid JSON", 400)
			return
		}
		if strings.TrimSpace(body.Message) == "" || !utf8.ValidString(body.Message) || len(body.Message) > 512 || strings.ContainsAny(body.Message, "\r\n") {
			http.Error(w, "invalid message", 400)
			return
		}
		if err := b.send(body.Message); err != nil {
			http.Error(w, "chat unavailable", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return mux
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		_, port, err := net.SplitHostPort(env("HTTP_ADDRESS", ":8080"))
		if err != nil {
			os.Exit(1)
		}
		resp, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
		if err != nil {
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	c, err := loadConfig()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	b := &bot{}
	server := &http.Server{Addr: c.httpAddress, Handler: b.handler(c.apiToken), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	go func() { defer close(done); b.run(ctx, c) }()
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server failed", "error", err)
			cancel()
		}
	}()
	slog.Info("bot service started", "http", c.httpAddress, "auth", c.mode)
	<-ctx.Done()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = server.Shutdown(shutdown)
	select {
	case <-done:
	case <-shutdown.Done():
	}
}
