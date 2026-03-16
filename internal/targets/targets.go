package targets

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"SpeakersRC/internal/config"
)

type APIResponse struct {
	Result int    `json:"result"`
	Reason string `json:"reason"`
}

var (
	cfg   *config.Config
	cfgMu sync.RWMutex
)

func SetConfig(c *config.Config) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg = c
}

func buildTargetURL(targetName string, target *config.TargetConfig, queryPath string) string {
	return fmt.Sprintf("%s://%s%s", target.Protocol, targetName, queryPath)
}

type musicFileResponse struct {
	Result int `json:"result"`
	Data   struct {
		Musicfile []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"musicfile"`
	} `json:"data"`
}

func loadMusicFiles(targetName string, target *config.TargetConfig) error {
	configUrl := buildTargetURL(targetName, target, target.ConfigQuery)
	resp, err := http.Get(configUrl)
	if err != nil {
		return fmt.Errorf("не удалось получить файлы с %s: %w", configUrl, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа: %w", err)
	}

	var mfr musicFileResponse
	if err := json.Unmarshal(body, &mfr); err != nil {
		return fmt.Errorf("ошибка парсинга JSON: %w", err)
	}

	reMP3 := regexp.MustCompile(`\.mp3$`)
	files := []config.MusicFile{}

	for _, f := range mfr.Data.Musicfile {
		if f.Name == "" {
			continue
		}
		name := reMP3.ReplaceAllString(f.Name, "")
		desc := reMP3.ReplaceAllString(f.Description, "")
		descNum := 0
		if strings.HasPrefix(desc, "userfile") {
			if n, err := strconv.Atoi(desc[len("userfile"):]); err == nil {
				descNum = n
			}
		}
		files = append(files, config.MusicFile{
			Name:        name,
			Description: fmt.Sprintf("userfile%d.mp3", descNum),
		})
	}

	target.MusicFiles = files
	return nil
}

func PreloadFiles() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	for name, t := range cfg.Targets {
		if err := loadMusicFiles(name, t); err != nil {
			log.Printf("Ошибка загрузки файлов для %s: %v", name, err)
		}
	}
}

func getFileDescription(target *config.TargetConfig, fileName string) string {
	reMP3 := regexp.MustCompile(`\.mp3$`)
	for _, f := range target.MusicFiles {
		if f.Name == fileName {
			desc := reMP3.ReplaceAllString(f.Description, "")
			if strings.HasPrefix(desc, "userfile") {
				if n, err := strconv.Atoi(desc[len("userfile"):]); err == nil {
					return fmt.Sprintf("userfile%d", n+1)
				}
			}
			return reMP3.ReplaceAllString(f.Description, "")
		}
	}
	return reMP3.ReplaceAllString(fileName, "")
}

// ProxyHandler возвращает хендлер /api/{target}/play
func ProxyHandler(playQuery, stopQuery string, params map[string]config.ParamConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// alias приходит из query-параметра target (JS шлёт ?target=host1)
		alias := r.URL.Query().Get("target")
		file := r.URL.Query().Get("file")
		action := r.URL.Query().Get("action")

		if alias == "" {
			http.Error(w, "Target required", http.StatusBadRequest)
			return
		}

		cfgMu.RLock()
		// по alias находим IP
		ip, ok := cfg.AliasToIP[alias]
		if !ok {
			cfgMu.RUnlock()
			http.Error(w, "Target not found", http.StatusBadRequest)
			return
		}

		target, exists := cfg.Targets[ip]
		cfgMu.RUnlock()
		if !exists || target == nil {
			http.Error(w, "Target not found", http.StatusBadRequest)
			return
		}

		if file == "" {
			http.Error(w, "File required", http.StatusBadRequest)
			return
		}

		client := &http.Client{Timeout: 30 * time.Second}
		baseQuery := playQuery
		if action == "stop" {
			baseQuery = stopQuery
		}

		// преобразуем «человеческое» имя файла в то, что ждёт колонка
		fileParam := getFileDescription(target, file)

		durationStr := r.URL.Query().Get("duration")
		multiplyStr := r.URL.Query().Get("multiply")
		duration, _ := strconv.Atoi(durationStr)
		multiply, _ := strconv.Atoi(multiplyStr)

		var modeParams string
		if multiply > 1 {
			modeParams = "mode=multiple&count=" + strconv.Itoa(multiply)
		} else if duration > 0 {
			modeParams = "mode=duration&count=" + strconv.Itoa(duration)
		} else {
			modeParams = "mode=once"
		}

		volumeStr := r.URL.Query().Get("volume")
		var volumeParam string
		if volumeStr != "" {
			volumeParam = "&volume=" + volumeStr
		}

		fullQuery := fmt.Sprintf("%s&file=%s&%s%s",
			baseQuery,
			url.QueryEscape(fileParam),
			modeParams,
			volumeParam,
		)

		// К железке идём по IP, а не по alias
		targetUrl := buildTargetURL(ip, target, fullQuery)
		log.Printf("📡 %s %s → %s", alias, action, targetUrl)

		req, err := http.NewRequest("GET", targetUrl, nil)
		if err != nil {
			log.Printf("❌ Создание запроса %s: %v", targetUrl, err)
			http.Error(w, "Ошибка запроса", http.StatusInternalServerError)
			return
		}

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("❌ Сеть %s: %v", targetUrl, err)
			http.Error(w, "Ошибка сети", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		var apiResp APIResponse
		_ = json.Unmarshal(body, &apiResp)

		if apiResp.Result == 0 {
			log.Printf("✅ УСПЕХ %s %s → %s\n%s", alias, action, targetUrl, apiResp.Reason)
			_, _ = w.Write([]byte("Успешно"))
		} else {
			log.Printf("❌ ОШИБКА %s %s → %s\n%s (result=%d)",
				alias, action, targetUrl, apiResp.Reason, apiResp.Result)
			_, _ = w.Write([]byte("Ошибка"))
		}
	}
}

// FilesHandler возвращает хендлер /api/{target}/files
func FilesHandler(alias string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfgMu.RLock()
		ip, ok := cfg.AliasToIP[alias]
		if !ok {
			cfgMu.RUnlock()
			http.Error(w, "Target not found", http.StatusBadRequest)
			return
		}

		target, exists := cfg.Targets[ip]
		if !exists {
			cfgMu.RUnlock()
			http.Error(w, "Target not found", http.StatusBadRequest)
			return
		}

		files := target.MusicFiles
		cfgMu.RUnlock()

		resp := map[string]interface{}{
			"target": alias, // во фронт отдаём alias
			"files":  files,
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
