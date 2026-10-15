package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

type application struct {
	store             confessionStore
	salt              string
	trustProxy        bool
	limiter           *IPRateLimiter
	rateLimitRequests int
	now               func() time.Time
}

type createRequest struct {
	Text     string `json:"text"`
	Category string `json:"category"`
	Website  string `json:"website"`
}

func main() {
	cfg, err := loadConfig(".env")
	if err != nil {
		log.Fatal(err)
	}

	startupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := NewPostgresStore(startupCtx, cfg.DatabaseURL, cfg.SubmitMinInterval, cfg.SubmitDailyLimit)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(startupCtx); err != nil {
		log.Fatal(err)
	}
	if err := store.Seed(startupCtx); err != nil {
		log.Fatal(err)
	}

	app := &application{
		store:             store,
		salt:              cfg.IPHashSalt,
		trustProxy:        cfg.TrustProxy,
		limiter:           NewIPRateLimiter(cfg.RateLimitRequests, cfg.RateLimitWindow, cfg.RateLimitBurst),
		rateLimitRequests: cfg.RateLimitRequests,
		now:               time.Now,
	}
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("silent room: http://localhost:%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("ошибка остановки сервера: %v", err)
	}
}

func (app *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/confessions", app.listConfessions)
	mux.HandleFunc("POST /api/confessions", app.createConfession)
	mux.HandleFunc("POST /api/confessions/{id}/view", app.recordView)
	mux.HandleFunc("GET /api/stats", app.stats)

	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return app.securityHeaders(app.rateLimit(mux))
}

func (app *application) listConfessions(w http.ResponseWriter, r *http.Request) {
	sort := r.URL.Query().Get("sort")
	category := r.URL.Query().Get("category")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items, err := app.store.List(r.Context(), sort, category, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось загрузить признания")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (app *application) createConfession(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input createRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "Не удалось прочитать признание")
		return
	}
	if strings.TrimSpace(input.Website) != "" {
		writeError(w, http.StatusBadRequest, "Не удалось отправить признание")
		return
	}

	text := strings.TrimSpace(input.Text)
	if len([]rune(text)) < 20 {
		writeError(w, http.StatusBadRequest, "Напишите хотя бы 20 символов")
		return
	}
	if len([]rune(text)) > 2000 {
		writeError(w, http.StatusBadRequest, "Признание не должно превышать 2000 символов")
		return
	}
	if !validCategory(input.Category) {
		writeError(w, http.StatusBadRequest, "Выберите существующую категорию")
		return
	}

	ipHash := app.hashIP(app.clientIP(r))
	confession, retryAfter, err := app.store.Create(r.Context(), text, input.Category, ipHash, app.now().UTC())
	if errors.Is(err, ErrRateLimited) {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("Слишком часто. Попробуйте через %s", humanDuration(retryAfter)))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось сохранить признание")
		return
	}
	writeJSON(w, http.StatusCreated, confession)
}

func (app *application) recordView(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "Неизвестное признание")
		return
	}
	views, err := app.store.RecordView(r.Context(), id, app.hashIP(app.clientIP(r)), app.now().UTC())
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "Признание не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось учесть просмотр")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"views": views})
}

func (app *application) stats(w http.ResponseWriter, r *http.Request) {
	stats, err := app.store.Stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось загрузить статистику")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (app *application) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		allowed, retryAfter := app.limiter.Allow(app.clientIP(r), app.now())
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(app.rateLimitRequests))
		if !allowed {
			seconds := int(retryAfter.Round(time.Second).Seconds())
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeError(w, http.StatusTooManyRequests, "Слишком много запросов. Попробуйте немного позже")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (app *application) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (app *application) hashIP(ip string) string {
	sum := sha256.Sum256([]byte(app.salt + "|" + ip))
	return hex.EncodeToString(sum[:])
}

func (app *application) clientIP(r *http.Request) string {
	// Proxy headers are trusted only when explicitly enabled behind a known proxy.
	if app.trustProxy {
		// Railway publishes the original client address in X-Real-IP.
		if ip := normalizedIP(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]; forwarded != "" {
			if ip := normalizedIP(forwarded); ip != "" {
				return ip
			}
		}
	}
	if ip := normalizedIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

func normalizedIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.String()
	}
	return ""
}

func validCategory(category string) bool {
	switch category {
	case "поступок", "отношения", "тайна", "мысли", "другое":
		return true
	default:
		return false
	}
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return "несколько секунд"
	}
	minutes := int(d.Round(time.Minute).Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%d мин.", minutes)
	}
	return fmt.Sprintf("%d ч.", int(d.Round(time.Hour).Hours()))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
