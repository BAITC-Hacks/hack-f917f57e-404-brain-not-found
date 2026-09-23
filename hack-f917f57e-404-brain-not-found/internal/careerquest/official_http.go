package careerquest

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

type pendingImport struct {
	Dataset     Dataset
	Summary     ImportSummary
	Version     StoreVersion
	Account     string
	AuthVersion uint64
	CSRF        string
	Expires     time.Time
	Replace     bool
}

func (a *App) previewOfficialImport(w http.ResponseWriter, r *http.Request, s Session) {
	mode := r.FormValue("mode")
	if mode != "replace" && mode != "append" {
		http.Error(w, "Choose replace or append import mode", 400)
		return
	}
	file, _, err := r.FormFile("dataset")
	if err != nil {
		http.Error(w, "Choose the official ZIP package or employee update ZIP", 400)
		return
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, (20<<20)+1))
	if err != nil || len(b) > 20<<20 {
		http.Error(w, "ZIP must be no larger than 20 MB", 400)
		return
	}
	base, version := a.store.snapshotVersioned()
	var dataset Dataset
	var summary ImportSummary
	if mode == "replace" {
		dataset, summary, err = decodeOfficialZip(bytes.NewReader(b), int64(len(b)))
	} else {
		dataset, summary, err = prepareJuryZip(base, bytes.NewReader(b), int64(len(b)))
	}
	if err != nil {
		http.Error(w, "Import rejected: "+err.Error(), 400)
		return
	}
	pending := pendingImport{Dataset: dataset, Summary: summary, Version: version, Account: s.Account, AuthVersion: s.AuthVersion, CSRF: s.CSRF, Expires: time.Now().Add(10 * time.Minute), Replace: mode == "replace"}
	token := randomID()
	a.mu.Lock()
	for key, p := range a.pendingImports {
		if time.Now().After(p.Expires) {
			delete(a.pendingImports, key)
		}
	}
	if len(a.pendingImports) >= 8 {
		a.mu.Unlock()
		http.Error(w, "Too many pending imports. Allow old previews to expire.", 429)
		return
	}
	a.pendingImports[token] = pending
	a.mu.Unlock()
	language, theme := a.displayPreferences(w, r)
	var rendered bytes.Buffer
	if err = a.templates[language].ExecuteTemplate(&rendered, "import-preview.html", struct {
		Pending         pendingImport
		Token, CSRF     string
		Language, Theme string
	}{pending, token, s.CSRF, language, theme}); err != nil {
		http.Error(w, "Unable to render import preview", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rendered.WriteTo(w)
}

func (a *App) commitOfficialImport(w http.ResponseWriter, r *http.Request, s Session) {
	if r.FormValue("confirm") != "import" {
		http.Error(w, "Confirm the validated import", 400)
		return
	}
	token := r.FormValue("preview")
	a.mu.Lock()
	pending, ok := a.pendingImports[token]
	if ok && pending.Account == s.Account && pending.AuthVersion == s.AuthVersion && pending.CSRF == s.CSRF {
		delete(a.pendingImports, token)
	} else {
		ok = false
	}
	a.mu.Unlock()
	if !ok || time.Now().After(pending.Expires) {
		http.Error(w, "Import preview expired or belongs to another session. Preview the ZIP again.", 409)
		return
	}
	if err := a.store.commitImportedDataset(pending.Dataset, pending.Version, pending.Replace); err != nil {
		if err == ErrStaleRecommendation || err == ErrStaleIdentity {
			http.Error(w, "Data changed after preview. Preview the ZIP again.", 409)
			return
		}
		http.Error(w, "Import could not be committed; original data retained", 500)
		return
	}
	if pending.Replace {
		a.mu.Lock()
		for id, session := range a.sessions {
			if session.Role == "employee" {
				delete(a.sessions, id)
			}
		}
		a.mu.Unlock()
	}
	redirect(w, r, "Official dataset import completed. Review the profiles and provision employee accounts where needed.")
}
