package careerquest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func presentationTestApp(t *testing.T) *App {
	t.Helper()
	d := Dataset{
		DataEpoch:    1,
		Employees:    []Employee{{ID: "employee", Name: "Test Employee", Role: "Developer", Grade: 1, Version: 1, Skills: map[string]int{"Go": 2, "SQL": 5}}},
		Requirements: []Requirement{{Role: "Developer", Grade: 2, Skills: map[string]int{"Go": 4, "SQL": 5, "Writing": 3}}},
		Activities: []Activity{
			{ID: "go-course", Title: "Go Workshop", Description: "Build services", Format: "Course", Hours: 2, MinGrade: 1, MaxGrade: 5, Growth: map[string]Growth{"Go": {Gain: 2, MaxLevel: 5}}},
			{ID: "sql-course", Title: "SQL Workshop", Format: "Course", Hours: 2, MinGrade: 1, MaxGrade: 5, Growth: map[string]Growth{"SQL": {Gain: 1, MaxLevel: 6}}},
		},
		Recommendations: map[string]RecommendationSet{},
	}
	app, err := newApp(&Store{data: d, path: filepath.Join(t.TempDir(), "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	app.accounts = &AccountStore{accounts: map[string]Account{
		"employee": {Username: "employee", EmployeeID: "employee", Role: "employee", Version: 1, DataEpoch: 1},
		"hr":       {Username: "hr", Role: "hr", Version: 1},
	}}
	for _, role := range []string{"employee", "hr"} {
		account := app.accounts.accounts[role]
		app.sessions[role] = Session{Account: role, Role: role, EmployeeID: account.EmployeeID, AuthVersion: 1, DataEpoch: 1, CSRF: "test-token", Expires: time.Now().Add(time.Hour)}
	}
	return app
}

func pageRequest(app *App, target, role string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if role != "" {
		r.AddCookie(&http.Cookie{Name: "career_session", Value: role})
	}
	w := httptest.NewRecorder()
	app.routes().ServeHTTP(w, r)
	return w
}

func TestFrontendPagesAndLanguages(t *testing.T) {
	app := presentationTestApp(t)
	for _, language := range []string{"en", "kk", "ru"} {
		for _, page := range []struct{ role, view string }{{"", ""}, {"hr", "hr"}, {"employee", "overview"}, {"employee", "recommendations"}, {"employee", "activities"}, {"employee", "history"}} {
			t.Run(language+"/"+page.role+"/"+page.view, func(t *testing.T) {
				t.Parallel()
				w := pageRequest(app, "/?lang="+language+"&theme=dark&view="+page.view, page.role)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `lang="`+language+`" data-theme="dark"`) {
					t.Fatalf("page failed: %d %s", w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "<no value>") || strings.Contains(w.Body.String(), `href="/sample"`) {
					t.Fatal("page contains missing data or a broken sample link")
				}
			})
		}
	}
}

func TestPreferencesPersistAndRejectUnknownValues(t *testing.T) {
	app := presentationTestApp(t)
	w := pageRequest(app, "/?lang=kk&theme=dark", "")
	r := httptest.NewRequest(http.MethodGet, "/?lang=invalid&theme=invalid", nil)
	for _, cookie := range w.Result().Cookies() {
		r.AddCookie(cookie)
	}
	w = httptest.NewRecorder()
	app.routes().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `lang="kk" data-theme="dark"`) || !strings.Contains(w.Body.String(), "Қазақша") {
		t.Fatal("preferences were not retained")
	}
	w = pageRequest(app, "/?lang=invalid&theme=invalid", "")
	if !strings.Contains(w.Body.String(), `lang="en" data-theme="light"`) {
		t.Fatal("invalid preferences must fall back to defaults")
	}
}

func TestActivitySearchAndOverview(t *testing.T) {
	app := presentationTestApp(t)
	for _, query := range []string{"q=gO", "q=services", "filter=useful"} {
		w := pageRequest(app, "/?view=activities&"+query, "employee")
		if !strings.Contains(w.Body.String(), "Go Workshop") || strings.Contains(w.Body.String(), "SQL Workshop") {
			t.Fatalf("filter failed: %s", query)
		}
	}
	w := pageRequest(app, "/?view=activities&q=not-found", "employee")
	if !strings.Contains(w.Body.String(), "No activities match your search.") {
		t.Fatal("missing empty search state")
	}
	w = pageRequest(app, "/?view=unknown", "employee")
	if !strings.Contains(w.Body.String(), `aria-current="page"`) || !strings.Contains(w.Body.String(), "Your next step is clear.") {
		t.Fatal("unknown view must fall back to overview")
	}
	p := Page{Context: contextFor(app.store.data, app.store.data.Employees[0])}
	populateEmployeePresentation(&p, httptest.NewRequest(http.MethodGet, "/", nil))
	if p.Met != 1 || p.Gaps != 1 || p.TargetCount != 3 || p.Context.Missing != 1 || p.TargetGrade != "2" {
		t.Fatalf("incorrect assessment summary: %+v", p)
	}
	w = pageRequest(app, "/?view=activities&q="+url.QueryEscape(`"><script>alert(1)</script>`), "employee")
	if strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("search text is not escaped")
	}
}

func TestEmbeddedAssetsAndImportPreview(t *testing.T) {
	app := presentationTestApp(t)
	for _, asset := range []string{"styles.css", "themes.css", "halyk.css", "server.css", "assets/manrope-400.ttf", "assets/manrope-600.ttf", "assets/manrope-700.ttf", "assets/manrope-800.ttf", "assets/Manrope-OFL.txt"} {
		w := pageRequest(app, "/static/"+asset, "")
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Fatalf("missing asset: %s", asset)
		}
	}
	for _, path := range []string{"/static/", "/static/assets/", "/static/translations.json", "/static/templates/index.html"} {
		if w := pageRequest(app, path, ""); w.Code != http.StatusNotFound {
			t.Fatalf("unexpected public path: %s", path)
		}
	}
	for _, language := range []string{"en", "kk", "ru"} {
		var buf bytes.Buffer
		data := struct {
			Language, Theme, Token, CSRF string
			Pending                      pendingImport
		}{Language: language, Theme: "dark", Token: "preview", CSRF: "csrf", Pending: pendingImport{Replace: true}}
		if err := app.templates[language].ExecuteTemplate(&buf, "import-preview.html", data); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), `data-theme="dark"`) || !strings.Contains(buf.String(), `action="/import/commit"`) {
			t.Fatal("import preview is missing preferences or form")
		}
	}
}

func TestRecommendationAndCompletionNavigation(t *testing.T) {
	app := presentationTestApp(t)
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: "career_session", Value: "employee"})
		w := httptest.NewRecorder()
		app.routes().ServeHTTP(w, r)
		return w
	}
	w := post("/recommend", url.Values{"csrf": {"wrong"}})
	if w.Code != http.StatusForbidden {
		t.Fatal("CSRF protection changed")
	}
	w = post("/recommend", url.Values{"csrf": {"test-token"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "view=recommendations") {
		t.Fatalf("recommendation redirect: %d %s", w.Code, w.Body.String())
	}
	w = pageRequest(app, w.Header().Get("Location"), "employee")
	if !strings.Contains(w.Body.String(), "Go Workshop") || !strings.Contains(w.Body.String(), "RULES-BASED") {
		t.Fatal("generated recommendation not rendered")
	}
	w = post("/complete", url.Values{"csrf": {"test-token"}, "activity_id": {"go-course"}, "request_id": {randomID()}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "view=history") {
		t.Fatalf("completion redirect: %d %s", w.Code, w.Body.String())
	}
	w = pageRequest(app, w.Header().Get("Location"), "employee")
	if !strings.Contains(w.Body.String(), "go-course") || !strings.Contains(w.Body.String(), "2 → 4") {
		t.Fatal("completed activity and skill changes not shown in history")
	}
}

func TestRecommendationSourceLabel(t *testing.T) {
	app := presentationTestApp(t)
	for _, source := range []string{"ai-assisted", "rules-fallback", "rules-based"} {
		w := httptest.NewRecorder()
		app.render(w, Page{Language: "en", Theme: "light", View: "recommendations", LoggedIn: true, Recommendation: &RecommendationSet{Source: source}})
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "AI-ASSISTED") != (source == "ai-assisted") {
			t.Fatalf("incorrect source label for %s", source)
		}
	}
}
