package careerquest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Session struct {
	EmployeeID, Role, CSRF string
	Account                string
	AuthVersion, DataEpoch uint64
	Expires                time.Time
}

type App struct {
	secureCookies   bool
	store           *Store
	templates       map[string]*template.Template
	mu              sync.Mutex
	sessions        map[string]Session
	accounts        *AccountStore
	loginAttempts   map[string]loginAttempt
	pendingImports  map[string]pendingImport
	provider        SelectionProvider
	providerService *ProviderService
}

type ActivityView struct {
	Activity    Activity
	Changes     []Change
	Useful      int
	RequestID   string
	Projections []SkillProjection
}

type Page struct {
	Language, Theme, View, Query, ActivityFilter, TargetGrade string
	Met, Gaps, TargetCount                                    int
	Title, CSRF, Notice, Error                                string
	LoggedIn, IsHR                                            bool
	Employees                                                 []Employee
	Context                                                   EmployeeContext
	Recommendation                                            *RecommendationSet
	Activities                                                []ActivityView
	HR                                                        HRSummary
	EngagementFilter                                          string
	AIEnabled                                                 bool
}

func randomID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newApp(store *Store) (*App, error) {
	templates, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	return &App{
		store:           store,
		templates:       templates,
		sessions:        make(map[string]Session),
		loginAttempts:   make(map[string]loginAttempt),
		pendingImports:  make(map[string]pendingImport),
		providerService: newProviderService(),
	}, nil
}

func (a *App) getSession(r *http.Request) (Session, bool) {
	cookie, err := r.Cookie("career_session")
	if err != nil {
		return Session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[cookie.Value]
	if !ok || time.Now().After(s.Expires) {
		delete(a.sessions, cookie.Value)
		return Session{}, false
	}
	if s.Role != "" {
		account, valid := a.accounts.lookup(s.Account)
		if !valid || account.Version != s.AuthVersion || account.Role != s.Role || account.EmployeeID != s.EmployeeID || (s.Role == "employee" && (account.DataEpoch != s.DataEpoch || s.DataEpoch != a.store.dataEpoch())) {
			delete(a.sessions, cookie.Value)
			return Session{}, false
		}
	}
	return s, true
}

func (a *App) setSession(w http.ResponseWriter, r *http.Request, s Session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if old, err := r.Cookie("career_session"); err == nil {
		delete(a.sessions, old.Value)
	}
	for id, existing := range a.sessions {
		if time.Now().After(existing.Expires) {
			delete(a.sessions, id)
		}
	}
	id := randomID()
	s.Expires = time.Now().Add(8 * time.Hour)
	a.sessions[id] = s
	http.SetCookie(w, &http.Cookie{Name: "career_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || a.secureCookies, MaxAge: 28800})
}

func (a *App) render(w http.ResponseWriter, p Page) {
	var buf bytes.Buffer
	if err := a.templates[p.Language].ExecuteTemplate(&buf, "index.html", p); err != nil {
		log.Print(err)
		http.Error(w, "Unable to render page", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func redirect(w http.ResponseWriter, r *http.Request, message string) {
	values := url.Values{"notice": {message}}
	switch r.URL.Path {
	case "/recommend":
		values.Set("view", "recommendations")
	case "/complete":
		values.Set("view", "history")
	}
	http.Redirect(w, r, "/?"+values.Encode(), http.StatusSeeOther)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "web/static")
	staticFiles := http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, err := fs.Stat(static, strings.TrimPrefix(r.URL.Path, "/static/"))
		if err != nil || file.IsDir() {
			http.NotFound(w, r)
			return
		}
		staticFiles.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /{$}", a.home)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /logout", a.protect("", a.logout))
	mux.HandleFunc("POST /recommend", a.protect("employee", a.generate))
	mux.HandleFunc("POST /complete", a.protect("employee", a.complete))
	mux.HandleFunc("POST /import", a.protect("hr", a.importData))
	mux.HandleFunc("POST /accounts", a.protect("hr", a.provisionAccount))
	mux.HandleFunc("POST /import/preview", a.protect("hr", a.previewOfficialImport))
	mux.HandleFunc("POST /import/commit", a.protect("hr", a.commitOfficialImport))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method == http.MethodPost {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host || (u.Scheme != "https" && u.Scheme != "http") {
					http.Error(w, "Cross-origin request denied", 403)
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "Cross-site request denied", 403)
				return
			}
			limit := int64(2 << 20)
			if r.URL.Path == "/import/preview" {
				limit = 20 << 20
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *App) protect(role string, next func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.getSession(r)
		if !ok || s.Role == "" {
			http.Error(w, "Please sign in", 401)
			return
		}
		if role != "" && s.Role != role {
			http.Error(w, "This action requires a different role", 403)
			return
		}
		if r.Method == http.MethodPost {
			var err error
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				err = r.ParseMultipartForm(2 << 20)
				if r.MultipartForm != nil {
					defer r.MultipartForm.RemoveAll()
				}
			} else {
				err = r.ParseForm()
			}
			if err != nil {
				http.Error(w, "Invalid request or file larger than the upload limit", 400)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(s.CSRF)) != 1 {
				http.Error(w, "Invalid form token; reload the page", 403)
				return
			}
		}
		next(w, r, s)
	}
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	d := a.store.snapshot()
	s, ok := a.getSession(r)
	if !ok {
		s = Session{CSRF: randomID()}
		a.setSession(w, r, s)
	}
	language, theme := a.displayPreferences(w, r)
	p := Page{Language: language, Theme: theme, Title: "Career Quest", CSRF: s.CSRF, Notice: r.URL.Query().Get("notice"), AIEnabled: a.provider != nil}
	if s.Role == "" {

		a.render(w, p)
		return
	}
	p.LoggedIn = true
	p.IsHR = s.Role == "hr"
	if p.IsHR {
		p.View = "hr"
		p.HR = hrSummary(d)
		p.Employees = d.Employees
		p.EngagementFilter = r.URL.Query().Get("engagement")
		p.HR.Engagement = filterEngagementRows(p.HR.Engagement, p.EngagementFilter)
		a.render(w, p)
		return
	}
	e, ok := findEmployee(d, s.EmployeeID)
	if !ok {
		http.Error(w, "Profile was removed by an import. Sign in again at /.", 401)
		a.setSession(w, r, Session{CSRF: randomID()})
		return
	}
	p.Context = contextFor(d, e)
	populateEmployeePresentation(&p, r)
	if set, exists := d.Recommendations[e.ID]; exists && recommendationCurrent(d, e, set, datasetReferenceTime(d)) {
		p.Recommendation = &set
	}
	for _, activity := range d.Activities {
		if eligible(d, e, activity) {
			changes, closure := simulate(e, activity, p.Context.Gaps)
			if p.View == "activities" && (!matchesActivity(activity, p.Query) || (p.ActivityFilter == "useful" && closure == 0)) {
				continue
			}
			projections, _ := projectActivity(e, activity, p.Context.Gaps)
			p.Activities = append(p.Activities, ActivityView{Activity: activity, Changes: changes, Useful: closure, RequestID: randomID(), Projections: projections})
		}
	}
	a.render(w, p)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request, s Session) {
	a.setSession(w, r, Session{CSRF: randomID()})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) generate(w http.ResponseWriter, r *http.Request, session Session) {
	d, version := a.store.snapshotVersioned()
	employee, ok := findEmployee(d, session.EmployeeID)
	if !ok || version.Epoch != session.DataEpoch {
		http.Error(w, "Dataset changed; sign in again", http.StatusUnauthorized)
		return
	}
	reference := datasetReferenceTime(d)
	if remote, enabled := a.provider.(*OpenAIProvider); enabled {
		if cached, exists := d.Recommendations[employee.ID]; exists && cached.Source == "ai-assisted" && recommendationCurrent(d, employee, cached, reference) && cached.Evidence.Model == remote.model {
			redirect(w, r, "Your verified AI recommendations are up to date. No new model request was needed.")
			return
		}
	}
	_, err := a.providerService.Execute(r.Context(), d, employee, reference, a.provider, func(ctx context.Context, set RecommendationSet) error {
		return a.store.publishRecommendationContext(ctx, employee.ID, version, set)
	})
	if errors.Is(err, ErrStaleRecommendation) || errors.Is(err, ErrStaleIdentity) {
		redirect(w, r, "Your profile or dataset changed while recommendations were being generated. Please try again.")
		return
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Print("Recommendation could not be saved: ", err)
		http.Error(w, "Could not generate and save recommendations. Please try again.", http.StatusServiceUnavailable)
		return
	}
	redirect(w, r, "Your next steps are ready. Suggestions use the current profile.")
}

func (a *App) complete(w http.ResponseWriter, r *http.Request, s Session) {
	err := a.store.completeAtEpoch(s.EmployeeID, r.FormValue("activity_id"), r.FormValue("request_id"), s.DataEpoch)
	if err != nil {
		log.Print(err)
		redirect(w, r, "Completion could not be saved. Reload and check that the activity is still eligible.")
		return
	}
	redirect(w, r, "Completion saved. Skills and progress are updated; generate fresh next steps.")
}

func (a *App) importData(w http.ResponseWriter, r *http.Request, s Session) {
	file, _, err := r.FormFile("dataset")
	if err != nil {
		redirect(w, r, "Choose a JSON dataset to import.")
		return
	}
	defer file.Close()
	var d Dataset
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&d); err != nil {
		redirect(w, r, "Invalid JSON dataset: "+err.Error())
		return
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		redirect(w, r, "Dataset must contain exactly one JSON object.")
		return
	}
	if err = validateDataset(d); err != nil {
		redirect(w, r, "Import rejected: "+err.Error())
		return
	}
	// Imported history is a record, not an instruction to re-apply historical gains.
	d.Recommendations = map[string]RecommendationSet{}
	for i := range d.Employees {
		d.Employees[i].Version = 1
	}
	if r.FormValue("confirm") != "replace" {
		redirect(w, r, "Confirm dataset replacement before importing.")
		return
	}
	if err = a.store.replaceDataset(d); err != nil {
		log.Print(err)
		http.Error(w, "Could not save import; original data kept", 500)
		return
	}
	// Invalidate employee sessions because an imported ID may describe a new person.
	a.mu.Lock()
	for id, session := range a.sessions {
		if session.Role == "employee" {
			delete(a.sessions, id)
		}
	}
	a.mu.Unlock()
	redirect(w, r, "Dataset validated and imported. Employee sessions and suggestions were reset.")
}
