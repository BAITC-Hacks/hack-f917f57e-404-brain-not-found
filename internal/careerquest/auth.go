package careerquest

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const passwordIterations = 600000

// Account identity is assigned by a trusted operator, never by a login request.
// Employee bindings include the durable dataset epoch so reused imported IDs do
// not silently become the same person's account.
type Account struct {
	Username   string `json:"username"`
	EmployeeID string `json:"employee_id,omitempty"`
	Role       string `json:"role"`
	Version    uint64 `json:"version"`
	DataEpoch  uint64 `json:"data_epoch,omitempty"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

type AccountStore struct {
	mu       sync.RWMutex
	path     string
	accounts map[string]Account
}

func openAccountStore(path string) (*AccountStore, error) {
	s := &AccountStore{path: path, accounts: map[string]Account{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &s.accounts); err != nil {
		return nil, fmt.Errorf("invalid private account database")
	}
	for key, a := range s.accounts {
		if key != a.Username || !validUsername(key) || (a.Role != "employee" && a.Role != "hr") || a.Version == 0 || a.Iterations != passwordIterations || len(a.Salt) != 48 || len(a.Hash) != 64 {
			return nil, fmt.Errorf("invalid private account record")
		}
	}
	return s, nil
}

func validUsername(s string) bool {
	if len(s) < 1 || len(s) > 80 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '@') {
			return false
		}
	}
	return true
}

func (s *AccountStore) lookup(username string) (Account, bool) {
	if s == nil {
		return Account{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[username]
	return a, ok
}

func (s *AccountStore) assign(username, employeeID, role, password string, epoch uint64) (Account, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !validUsername(username) || (role != "employee" && role != "hr") || len(password) < 16 || len(password) > 256 || (role == "employee" && (employeeID == "" || epoch == 0)) {
		return Account{}, fmt.Errorf("invalid account assignment")
	}
	salt := randomID()
	saltBytes, _ := hex.DecodeString(salt)
	key, err := pbkdf2.Key(sha256.New, password, saltBytes, passwordIterations, 32)
	if err != nil {
		return Account{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]Account, len(s.accounts)+1)
	for k, v := range s.accounts {
		next[k] = v
	}
	a := Account{Username: username, EmployeeID: employeeID, Role: role, Version: s.accounts[username].Version + 1, DataEpoch: epoch, Salt: salt, Hash: hex.EncodeToString(key), Iterations: passwordIterations}
	if role == "hr" {
		a.EmployeeID = ""
		a.DataEpoch = 0
	}
	next[username] = a
	if s.path != "" {
		b, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return Account{}, err
		}
		if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
			return Account{}, err
		}
		if err = os.WriteFile(s.path+".tmp", b, 0600); err != nil {
			return Account{}, err
		}
		if err = os.Rename(s.path+".tmp", s.path); err != nil {
			return Account{}, err
		}
	}
	s.accounts = next
	return a, nil
}

func (s *AccountStore) authenticate(username, password string) (Account, bool) {
	a, found := s.lookup(strings.ToLower(strings.TrimSpace(username)))
	if len(password) > 256 {
		return Account{}, false
	}
	// Unknown names perform the same KDF cost; responses never expose membership.
	salt := make([]byte, 24)
	expected := make([]byte, 32)
	if found {
		salt, _ = hex.DecodeString(a.Salt)
		expected, _ = hex.DecodeString(a.Hash)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil || subtle.ConstantTimeCompare(key, expected) != 1 || !found {
		return Account{}, false
	}
	return a, true
}

type loginAttempt struct {
	Count int
	Until time.Time
}

func (a *App) allowLogin(r *http.Request, username string) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, attempt := range a.loginAttempts {
		if now.After(attempt.Until) {
			delete(a.loginAttempts, key)
		}
	}
	if len(a.loginAttempts) > 4096 {
		return false
	}
	keys := []string{"account:" + strings.ToLower(strings.TrimSpace(username)), "ip:" + ip}
	for i, key := range keys {
		limit := 8
		if i == 1 {
			limit = 40
		}
		if a.loginAttempts[key].Count >= limit {
			return false
		}
	}
	for _, key := range keys {
		attempt := a.loginAttempts[key]
		if attempt.Count == 0 {
			attempt.Until = now.Add(10 * time.Minute)
		}
		attempt.Count++
		a.loginAttempts[key] = attempt
	}
	return true
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	s, ok := a.getSession(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	if !ok || s.CSRF == "" || subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(r.FormValue("csrf"))) != 1 {
		http.Error(w, "Reload the login page", 403)
		return
	}
	username := r.FormValue("account")
	if len(username) > 80 {
		redirect(w, r, "Sign-in failed. Check your assigned credentials.")
		return
	}
	if !a.allowLogin(r, username) {
		w.Header().Set("Retry-After", "600")
		http.Error(w, "Too many sign-in attempts. Try again in ten minutes.", 429)
		return
	}
	account, valid := a.accounts.authenticate(username, r.FormValue("password"))
	if valid && account.Role == "employee" {
		_, exists := findEmployee(a.store.snapshot(), account.EmployeeID)
		valid = exists && account.DataEpoch == a.store.dataEpoch()
	}
	if !valid {
		redirect(w, r, "Sign-in failed. Check your assigned credentials.")
		return
	}
	a.mu.Lock()
	delete(a.loginAttempts, "account:"+strings.ToLower(strings.TrimSpace(username)))
	a.mu.Unlock()
	a.setSession(w, r, Session{Account: account.Username, AuthVersion: account.Version, EmployeeID: account.EmployeeID, Role: account.Role, DataEpoch: account.DataEpoch, CSRF: randomID()})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) provisionAccount(w http.ResponseWriter, r *http.Request, s Session) {
	username := strings.ToLower(strings.TrimSpace(r.FormValue("account")))
	employeeID := r.FormValue("employee_id")
	if old, ok := a.accounts.lookup(username); ok && old.Role == "hr" {
		http.Error(w, "HR credentials can only be changed by the local operator", 403)
		return
	}
	d, version := a.store.snapshotVersioned()
	if _, ok := findEmployee(d, employeeID); !ok {
		http.Error(w, "Select an existing employee profile", 400)
		return
	}
	password := randomID()
	account, err := a.accounts.assign(username, employeeID, "employee", password, version.Epoch)
	if err != nil {
		http.Error(w, "Unable to provision account; use a valid unique username", 400)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="career-quest-private-credentials.txt"`)
	_, _ = fmt.Fprintf(w, "PRIVATE — deliver only to the assigned employee.\nUsername: %s\nPassword: %s\nEmployee profile: %s\nKeep this file private and remove it after secure delivery.\n", account.Username, password, account.EmployeeID)
}

// Initial secrets are written only to an ignored private delivery file, never
// into the application bundle, account database, logs, or default passwords.
func (a *App) initializeAccounts(privateDir string) error {
	a.accounts.mu.RLock()
	empty := len(a.accounts.accounts) == 0
	a.accounts.mu.RUnlock()
	if !empty {
		return nil
	}
	if err := os.MkdirAll(privateDir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(privateDir, "initial-credentials.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("private credential delivery file already exists or is inaccessible; inspect it before initializing")
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, "PRIVATE INITIAL ACCOUNT DELIVERY — remove after private delivery.")
	d, version := a.store.snapshotVersioned()
	employees := append([]Employee(nil), d.Employees...)
	sort.Slice(employees, func(i, j int) bool { return employees[i].ID < employees[j].ID })
	for _, e := range employees {
		password := randomID()
		username := strings.ToLower(e.ID)
		_, alreadyAssigned := a.accounts.lookup(username)
		if !validUsername(username) || username == "hr" || alreadyAssigned {
			username = "employee-" + randomID()[:12]
		}
		if _, err = a.accounts.assign(username, e.ID, "employee", password, version.Epoch); err != nil {
			return err
		}
		if _, err = fmt.Fprintf(f, "Username: %s\nPassword: %s\nEmployee profile: %s\n\n", username, password, e.ID); err != nil {
			return err
		}
	}
	password := randomID()
	if _, err = a.accounts.assign("hr", "", "hr", password, 0); err != nil {
		return err
	}
	if _, err = fmt.Fprintf(f, "Username: hr\nPassword: %s\nRole: HR\n", password); err != nil {
		return err
	}
	return f.Sync()
}

func (a *App) provisionCLI(username, target, privateDir string) error {
	role := "employee"
	d, version := a.store.snapshotVersioned()
	if target == "hr" {
		role = "hr"
	} else if _, ok := findEmployee(d, target); !ok {
		return fmt.Errorf("unknown employee profile")
	}
	password := randomID()
	if err := os.MkdirAll(privateDir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(privateDir, "delivery-"+randomID()+".txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	account, err := a.accounts.assign(username, target, role, password, version.Epoch)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(f, "PRIVATE — deliver only to the assigned user.\nUsername: %s\nPassword: %s\nRole: %s\nEmployee profile: %s\n", account.Username, password, account.Role, account.EmployeeID); err != nil {
		return err
	}
	return f.Sync()
}
