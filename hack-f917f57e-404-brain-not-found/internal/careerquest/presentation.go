package careerquest

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

// Each language has its own immutable template set. Concurrent requests cannot
// change the translation function used by another visitor.
func loadTemplates() (map[string]*template.Template, error) {
	data, err := assets.ReadFile("web/translations.json")
	if err != nil {
		return nil, err
	}
	var translations map[string]map[string]string
	if err := json.Unmarshal(data, &translations); err != nil {
		return nil, err
	}
	result := make(map[string]*template.Template)
	for _, language := range []string{"en", "kk", "ru"} {
		dictionary := translations[language]
		funcs := template.FuncMap{
			"tr": func(text string) string {
				if translated := dictionary[text]; translated != "" {
					return translated
				}
				return text
			},
			"add": func(a, b int) int { return a + b },
		}
		pages, err := template.New("pages").Funcs(funcs).ParseFS(assets, "web/templates/*.html")
		if err != nil {
			return nil, err
		}
		result[language] = pages
	}
	return result, nil
}

func (a *App) displayPreferences(w http.ResponseWriter, r *http.Request) (string, string) {
	preference := func(name, fallback string, allowed ...string) string {
		valid := func(value string) bool {
			for _, choice := range allowed {
				if value == choice {
					return true
				}
			}
			return false
		}
		value := fallback
		if cookie, err := r.Cookie("career_" + name); err == nil && valid(cookie.Value) {
			value = cookie.Value
		}
		if requested := r.URL.Query().Get(name); r.Method == http.MethodGet && valid(requested) {
			value = requested
			http.SetCookie(w, &http.Cookie{Name: "career_" + name, Value: value, Path: "/", MaxAge: 365 * 24 * 60 * 60, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || a.secureCookies})
		}
		return value
	}
	return preference("lang", "en", "en", "kk", "ru"), preference("theme", "light", "light", "dark")
}

func populateEmployeePresentation(p *Page, r *http.Request) {
	switch r.URL.Query().Get("view") {
	case "recommendations", "activities", "history":
		p.View = r.URL.Query().Get("view")
	default:
		p.View = "overview"
	}
	p.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if query := []rune(p.Query); len(query) > 200 {
		p.Query = string(query[:200])
	}
	p.ActivityFilter = "all"
	if r.URL.Query().Get("filter") == "useful" {
		p.ActivityFilter = "useful"
	}
	if p.Context.HasTarget {
		p.TargetGrade = strconv.Itoa(p.Context.Employee.Grade + 1)
	}
	for _, skill := range p.Context.Gaps {
		p.TargetCount++
		if skill.Assessed {
			if skill.Gap == 0 {
				p.Met++
			} else {
				p.Gaps++
			}
		}
	}
}

func matchesActivity(activity Activity, query string) bool {
	text := activity.ID + " " + activity.Title + " " + activity.Description + " " + activity.Format
	for skill := range activity.Growth {
		text += " " + skill
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(query))
}
