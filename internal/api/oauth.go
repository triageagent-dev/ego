// Package api's oauth.go implements this service's own GitHub OAuth
// authorization-code flow: /auth/login redirects to GitHub, /auth/callback
// exchanges the code, verifies org membership, and mints the same signed
// ego_session cookie sessionauth.go already knows how to validate.
//
// One deliberate simplification vs.
package api

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHubOAuthConfig holds this service's own GitHub OAuth App credentials
// and the org whose active membership gates access. Constructing one (via
// GitHubOAuthConfigFromParams) is what turns the auth gate on; see Serve.
type GitHubOAuthConfig struct {
	ClientID      string
	ClientSecret  string
	Org           string
	SessionSecret string
	PublicURL     string // e.g. "https://your-app.example.com", no trailing slash
}

const (
	sessionTTL = 12 * time.Hour
	stateTTL   = 10 * time.Minute

	stateCookieName = "ego_oauth_state"
)

// githubAuthorizeURL/githubTokenURL/githubAPIBaseURL are vars, not consts,
// solely so tests can point them at an httptest.Server instead of real
// GitHub.
var (
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubTokenURL     = "https://github.com/login/oauth/access_token"
	githubAPIBaseURL   = "https://api.github.com"
)

func GitHubOAuthConfigFromParams(clientID, clientSecret, org, sessionSecret, publicURL string) (*GitHubOAuthConfig, error) {
	if clientID == "" || clientSecret == "" {
		return nil, nil
	}
	if org == "" {
		return nil, fmt.Errorf("github client id/secret are set but no org was given: an org-less GitHub OAuth gate authenticates but does not authorize - refusing to start rather than silently letting any GitHub user in")
	}
	if sessionSecret == "" {
		return nil, fmt.Errorf("github client id is set but no session secret was given")
	}
	publicURL = strings.TrimRight(publicURL, "/")
	if publicURL == "" {
		return nil, fmt.Errorf("github client id is set but no public URL was given: this service needs an exact redirect_uri to send GitHub, and derives no fallback from the incoming request (see oauth.go's package doc)")
	}
	return &GitHubOAuthConfig{
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		Org:           org,
		SessionSecret: sessionSecret,
		PublicURL:     publicURL,
	}, nil
}

func (c *GitHubOAuthConfig) callbackURL() string {
	return c.PublicURL + "/auth/callback"
}

func mintSessionCookie(username, secret string) string {
	return MintSessionToken(username, secret, sessionTTL)
}

// MintSessionToken signs a token in the same username|expiry|hmac format mintSessionCookie/readSessionCookie
// use, valid for ttl from now. Accepted anywhere a session cookie is: requireSession's Authorization: Bearer
// path, or the ego_session cookie itself.
func MintSessionToken(username, secret string, ttl time.Duration) string {
	expiry := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s|%d", username, expiry)
	sig := signSessionPayload(secret, payload)
	return base64.URLEncoding.EncodeToString([]byte(payload + "|" + sig))
}

// makeStateToken is an opaque CSRF nonce: set as an HttpOnly cookie before
// redirecting to GitHub, echoed back verbatim as ?state=, and only ever
// compared for equality against its own cookie (see verifyState) - unlike
// the session cookie, it needs no signature or cross-service portability,
// since this service both sets and checks it.
func makeStateToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func verifyState(cookieValue, queryValue string) bool {
	if cookieValue == "" || queryValue == "" {
		return false
	}
	return hmac.Equal([]byte(cookieValue), []byte(queryValue))
}

// oauthCookieOpts.
func setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func deleteCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1})
}

// Redirects to GitHub's OAuth authorize endpoint with a freshly minted CSRF state, which is also stashed in a
// short-lived cookie for authCallbackHandler to check against GitHub's echoed-back ?state=.
func authLoginHandler(cfg *GitHubOAuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := makeStateToken()
		if err != nil {
			http.Error(w, "failed to generate OAuth state", http.StatusInternalServerError)
			return
		}
		setCookie(w, stateCookieName, state, stateTTL)
		params := url.Values{
			"client_id":    {cfg.ClientID},
			"redirect_uri": {cfg.callbackURL()},
			"scope":        {"read:org"},
			"state":        {state},
		}
		http.Redirect(w, r, githubAuthorizeURL+"?"+params.Encode(), http.StatusFound)
	}
}

// Exchanges the code for an access token, looks up the authenticated user's login, then checks their membership
// in cfg.Org - per GitHub's API, only a 204 response means "confirmed active member"; a 302 (membership exists
// but this token can't see it) or 404 (not a member) must both deny, since "can't tell" is not a safe default
// for an access gate. On success, mints and sets the same signed session cookie requireSession validates, then
// redirects to /evals (this service's own canonical UI path now - see charts/ego's HTTPRoute). roleStore, if
// non-nil (RBAC configured - see server.go's Serve), also has this user's GitHub team memberships resolved and
// cached here - the only point this service ever holds their own GitHub access token, needed to let a
// team-based RoleBinding apply to their browser session at all (see roles.go's teamCacheTTL doc comment).
func authCallbackHandler(cfg *GitHubOAuthConfig, roleStore *RoleStore) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		cookieState := ""
		if c, err := r.Cookie(stateCookieName); err == nil {
			cookieState = c.Value
		}
		if code == "" || !verifyState(cookieState, state) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired OAuth state"})
			return
		}

		accessToken, err := exchangeCodeForToken(client, cfg, code)
		if err != nil {
			log.Printf("auth: GitHub OAuth token exchange failed: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "GitHub OAuth token exchange failed"})
			return
		}

		username, err := fetchGitHubLogin(client, accessToken)
		if err != nil {
			log.Printf("auth: fetching GitHub user failed: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to fetch GitHub user"})
			return
		}

		member, err := checkOrgMembership(client, cfg.Org, username, accessToken)
		if err != nil {
			log.Printf("auth: checking org membership failed: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to verify org membership"})
			return
		}
		if !member {
			log.Printf("auth: GitHub OAuth: %s denied (not an active member of org %s)", username, cfg.Org)
			writeJSON(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("GitHub user %q is not an active member of the %q org", username, cfg.Org)})
			return
		}

		if roleStore != nil {
			if teams, err := FetchUserTeams(client, cfg.Org, accessToken); err != nil {
				log.Printf("auth: resolving GitHub team memberships for %s failed (team-based role bindings won't apply this session): %v", username, err)
			} else {
				roleStore.setTeams(username, teams)
			}
		}

		log.Printf("auth: GitHub OAuth: %s authenticated (member of %s)", username, cfg.Org)
		setCookie(w, sessionCookieName, mintSessionCookie(username, cfg.SessionSecret), sessionTTL)
		deleteCookie(w, stateCookieName)
		http.Redirect(w, r, "/evals", http.StatusFound)
	}
}

func authLogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deleteCookie(w, sessionCookieName)
		http.Redirect(w, r, "/evals", http.StatusFound)
	}
}

func exchangeCodeForToken(client *http.Client, cfg *GitHubOAuthConfig, code string) (string, error) {
	form := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {cfg.callbackURL()},
	}
	req, err := http.NewRequest(http.MethodPost, githubTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub token endpoint returned %d", resp.StatusCode)
	}

	var data struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response (error=%q)", data.Error)
	}
	return data.AccessToken, nil
}

func fetchGitHubLogin(client *http.Client, accessToken string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, githubAPIBaseURL+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub user endpoint returned %d", resp.StatusCode)
	}

	var data struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.Login == "" {
		return "", fmt.Errorf("GitHub user endpoint returned no login")
	}
	return data.Login, nil
}

// teamsFetchMaxPages/teamsFetchPerPage bound GET /user/teams pagination -
// a user in an unusually large number of teams across many orgs can't
// make FetchUserTeams loop indefinitely; 500 teams (5 pages * 100) is far
// beyond any real GitHub org's team count.
const (
	teamsFetchMaxPages = 5
	teamsFetchPerPage  = 100
)

// FetchUserTeams returns the "org/slug" team memberships (filtered to
// org) of the GitHub user identified by accessToken, via GET /user/teams -
// the only GitHub API that lists a user's *own* teams without requiring
// org-admin privileges. Used exclusively to resolve team-based
// RoleBinding subjects (see roles.go); never to gate org membership
// itself - checkOrgMembership remains the sole authority for "is this
// user allowed in at all".
func FetchUserTeams(client *http.Client, org, accessToken string) ([]string, error) {
	var slugs []string
	for page := 1; page <= teamsFetchMaxPages; page++ {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/user/teams?per_page=%d&page=%d", githubAPIBaseURL, teamsFetchPerPage, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "token "+accessToken)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var batch []struct {
			Slug         string `json:"slug"`
			Organization struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&batch)
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusOK {
			return nil, fmt.Errorf("GitHub /user/teams returned %d", status)
		}
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, t := range batch {
			if strings.EqualFold(t.Organization.Login, org) {
				slugs = append(slugs, org+"/"+t.Slug)
			}
		}
		if len(batch) < teamsFetchPerPage {
			break
		}
	}
	return slugs, nil
}

func checkOrgMembership(client *http.Client, org, username, accessToken string) (bool, error) {
	membershipURL := fmt.Sprintf("%s/orgs/%s/members/%s", githubAPIBaseURL, url.PathEscape(org), url.PathEscape(username))
	req, err := http.NewRequest(http.MethodGet, membershipURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "token "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent, nil
}
