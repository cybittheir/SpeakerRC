package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"SpeakersRC/internal/config"
)

var (
	appCfg        config.AppConfig
	loginAttempts = make(map[string]int)
	loginLocks    = make(map[string]time.Time)
	loginMu       sync.Mutex

	sessions   = make(map[string]time.Time)
	sessionsMu sync.Mutex
)

func Init(ac config.AppConfig) {
	appCfg = ac
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// getClientIP получает реальный IP клиента (учитывая прокси)
func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.Split(xff, ",")[0]
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return xrip
	}
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		return ip[:idx]
	}
	return ip
}

// isIPLocked проверяет, заблокирован ли IP и возвращает (заблокирован, оставшиесяМинуты)
func isIPLocked(clientIP string) (bool, int) {
	loginMu.Lock()
	defer loginMu.Unlock()

	lockTime, exists := loginLocks[clientIP]
	if !exists {
		return false, 0
	}

	elapsed := time.Since(lockTime)
	lockDuration := time.Duration(appCfg.Bruteforce.LockoutMinutes) * time.Minute

	if elapsed < lockDuration {
		remaining := int((lockDuration - elapsed).Minutes())
		if remaining < 1 {
			remaining = 1
		}
		return true, remaining
	}

	delete(loginLocks, clientIP)
	delete(loginAttempts, clientIP)
	return false, 0
}

// incrementLoginAttempt увеличивает счётчик попыток
func incrementLoginAttempt(clientIP string) bool {
	loginMu.Lock()
	defer loginMu.Unlock()

	attempts := loginAttempts[clientIP] + 1
	loginAttempts[clientIP] = attempts

	if attempts >= appCfg.Bruteforce.MaxAttempts {
		loginLocks[clientIP] = time.Now()
		return false
	}
	return true
}

// Handler обрабатывает POST /auth
func Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := getClientIP(r)

	if locked, remaining := isIPLocked(clientIP); locked {
		resp := map[string]interface{}{
			"authenticated":    false,
			"locked":           true,
			"lockMinutes":      appCfg.Bruteforce.LockoutMinutes,
			"remainingMinutes": remaining,
			"message":          fmt.Sprintf("IP %s заблокирован. Осталось ~%d мин", clientIP, remaining),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(resp)
		return
	}

	var req struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	success := bcrypt.CompareHashAndPassword([]byte(appCfg.SecretHash), []byte(req.Secret)) == nil

	resp := map[string]interface{}{
		"authenticated": success,
		"locked":        false,
	}

	if success {
		sessionID := newSessionID()
		now := time.Now()

		sessionsMu.Lock()
		sessions[sessionID] = now
		sessionsMu.Unlock()

		loginMu.Lock()
		delete(loginAttempts, clientIP)
		delete(loginLocks, clientIP)
		loginMu.Unlock()

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    sessionID,
			Path:     "/",
			HttpOnly: true,
			Secure:   false,
		})
	} else {
		if incrementLoginAttempt(clientIP) {
			loginMu.Lock()
			attempts := loginAttempts[clientIP]
			loginMu.Unlock()

			remainingAttempts := appCfg.Bruteforce.MaxAttempts - attempts
			if remainingAttempts < 0 {
				remainingAttempts = 0
			}
			resp["remainingAttempts"] = remainingAttempts
			resp["message"] = fmt.Sprintf("Неверный пароль. Осталось попыток: %d", remainingAttempts)
		} else {
			resp["locked"] = true
			resp["lockMinutes"] = appCfg.Bruteforce.LockoutMinutes
			resp["message"] = fmt.Sprintf("Слишком много попыток. IP %s заблокирован на %d мин",
				clientIP, appCfg.Bruteforce.LockoutMinutes)
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

// LogoutHandler — /logout
func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil && c.Value != "" {
		sessionsMu.Lock()
		delete(sessions, c.Value)
		sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	w.Write([]byte("OK"))
}

// Middleware проверяет сессию и таймаут неактивности
func Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := cookie.Value

		sessionsMu.Lock()
		last, exists := sessions[sessionID]
		if !exists {
			sessionsMu.Unlock()
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		timeout := time.Duration(appCfg.SessionTimeoutMinutes) * time.Minute
		if time.Since(last) > timeout {
			delete(sessions, sessionID)
			sessionsMu.Unlock()

			http.SetCookie(w, &http.Cookie{
				Name:     "session",
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
			})
			http.Error(w, "Session expired", http.StatusUnauthorized)
			return
		}

		sessions[sessionID] = time.Now()
		sessionsMu.Unlock()

		next.ServeHTTP(w, r)
	}
}

// IsAuthenticated используется в web для главной страницы
func IsAuthenticated(r *http.Request) bool {
	c, err := r.Cookie("session")
	if err != nil || c.Value == "" {
		return false
	}
	sessionsMu.Lock()
	last, exists := sessions[c.Value]
	sessionsMu.Unlock()
	if !exists {
		return false
	}
	timeout := time.Duration(appCfg.SessionTimeoutMinutes) * time.Minute
	return time.Since(last) <= timeout
}
