package web

import (
	"html/template"
	"net/http"

	"SpeakersRC/internal/auth"
	"SpeakersRC/internal/config"
)

var tpl *template.Template

func InitTemplates(pattern string) error {
	var err error
	tpl, err = template.ParseGlob(pattern)
	return err
}

func ListenAndServe(addr string, handler http.Handler) error {
	return http.ListenAndServe(addr, handler)
}

func NewRouter(
	cfg *config.Config,
	authMiddleware func(http.HandlerFunc) http.HandlerFunc,
	proxyFactory func(playQuery, stopQuery string, params map[string]config.ParamConfig) http.HandlerFunc,
	filesHandlerFactory func(targetName string) http.HandlerFunc,
) *http.ServeMux {
	mux := http.NewServeMux()

	// Главная страница
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := struct {
			Authenticated         bool
			Targets               map[string]*config.TargetConfig
			RepeatDurationSeconds int
		}{
			Authenticated:         auth.IsAuthenticated(r),
			Targets:               cfg.Targets,
			RepeatDurationSeconds: cfg.App.RepeatDurationSeconds,
		}
		tpl.ExecuteTemplate(w, "index.html", data)
	})

	// Auth
	mux.HandleFunc("/auth", auth.Handler)
	mux.HandleFunc("/logout", auth.LogoutHandler)

	// Targets
	for ip, t := range cfg.Targets {
		alias := t.Alias

		// /api/{alias}/files
		mux.HandleFunc("/api/"+alias+"/files", authMiddleware(filesHandlerFactory(alias)))

		// /api/{alias}/play
		mux.HandleFunc("/api/"+alias+"/play", authMiddleware(
			proxyFactory(t.PlayQuery, t.StopQuery, t.Params),
		))

		_ = ip // если не нужен, чтобы не ругался компилятор
	}

	// /refresh-files
	mux.HandleFunc("/refresh-files", authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// вызывать targets.PreloadFiles снаружи, если нужно — но проще оставить как есть,
		// PreloadFiles дергается в main при старте, обновление — можно добавить через канал/функцию
		w.Write([]byte("TODO: refresh-files not wired in this minimal split"))
	}))

	return mux
}
