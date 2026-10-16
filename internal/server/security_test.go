package server

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestInviteOnlyRegistration(t *testing.T) {
	ts, srv := newTestServerWithAPI(t)
	srv.Registration = RegistrationInvite
	srv.SetupToken = "s3tup"

	// The first account needs the setup token and becomes admin.
	anon := &client{t: t, base: ts.URL}
	if cfg := anon.do("GET", "/auth/config", nil, 200); cfg["setup_token_required"] != true || cfg["registration"] != "invite" {
		t.Fatalf("auth config = %v", cfg)
	}
	anon.do("POST", "/auth/register", map[string]string{"username": "admin", "password": "password123"}, 403)
	anon.do("POST", "/auth/register", map[string]string{"username": "admin", "password": "password123", "setup_token": "wrong"}, 403)
	out := anon.do("POST", "/auth/register", map[string]string{"username": "admin", "password": "password123", "setup_token": "s3tup"}, 200)
	if get(out, "user", "is_admin") != true {
		t.Fatalf("first account is not admin: %v", out)
	}
	admin := &client{t: t, base: ts.URL, token: out["token"].(string)}

	// After that, only invites let people in, and each works once.
	anon.do("POST", "/auth/register", map[string]string{"username": "mallory", "password": "password123", "setup_token": "s3tup"}, 403)
	inv := admin.do("POST", "/invites", map[string]any{"note": "for dev"}, 201)
	if !strings.Contains(inv["url"].(string), "/login?invite=orbit_inv_") {
		t.Fatalf("invite url = %v", inv["url"])
	}
	secret := inv["secret"].(string)
	out = anon.do("POST", "/auth/register", map[string]string{"username": "dev", "password": "password123", "invite": secret}, 200)
	if get(out, "user", "is_admin") != false {
		t.Fatalf("invited account is admin: %v", out)
	}
	dev := &client{t: t, base: ts.URL, token: out["token"].(string)}
	anon.do("POST", "/auth/register", map[string]string{"username": "dev2", "password": "password123", "invite": secret}, 403)

	invites := admin.do("GET", "/invites", nil, 200)["items"].([]any)
	if len(invites) != 1 || invites[0].(map[string]any)["used_by"] != "dev" {
		t.Fatalf("invites = %v", invites)
	}
	// Only admins manage invites.
	dev.do("POST", "/invites", map[string]any{}, 403)
	dev.do("GET", "/invites", nil, 403)

	// A revoked invite no longer works.
	inv = admin.do("POST", "/invites", map[string]any{}, 201)
	admin.do("DELETE", "/invites/"+get(inv, "invite", "id").(string), nil, 204)
	anon.do("POST", "/auth/register", map[string]string{"username": "dev3", "password": "password123", "invite": inv["secret"].(string)}, 403)
}

func TestEnsureAdminOnlyOnEmptyInstance(t *testing.T) {
	ts, srv := newTestServerWithAPI(t)
	ctx := t.Context()
	if created, err := srv.EnsureAdmin(ctx, "Root", "password123"); err != nil || !created {
		t.Fatalf("EnsureAdmin = %v, %v", created, err)
	}
	if created, err := srv.EnsureAdmin(ctx, "other", "password123"); err != nil || created {
		t.Fatalf("second EnsureAdmin = %v, %v", created, err)
	}
	c := &client{t: t, base: ts.URL}
	if out := c.do("POST", "/auth/login", map[string]string{"username": "root", "password": "password123"}, 200); get(out, "user", "is_admin") != true {
		t.Fatalf("login = %v", out)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ts, srv := newTestServerWithAPI(t)
	register(t, ts, "alice")
	srv.limits.authUser = newLimiter(3, time.Hour)
	c := &client{t: t, base: ts.URL}
	for range 3 {
		c.do("POST", "/auth/login", map[string]string{"username": "alice", "password": "wrong-password"}, 401)
	}
	// Blocked now, even with the right password.
	c.do("POST", "/auth/login", map[string]string{"username": "alice", "password": "password123"}, 429)

	// Git over HTTPS counts too.
	req, _ := http.NewRequest("GET", ts.URL+"/git/ledger/api.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("alice", "password123")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("git status = %d, want 429", res.StatusCode)
	}
}

func TestWebhooksRefusePrivateAddresses(t *testing.T) {
	ts, srv := newTestServerWithAPI(t)
	srv.WebhookAllowedNetworks = nil
	admin, _ := register(t, ts, "admin")
	team := admin.do("POST", "/teams", map[string]any{"slug": "payments", "name": "Payments"}, 201)
	admin.do("POST", "/apps", map[string]any{"key": "ledger", "name": "Ledger", "owner_team_id": get(team, "team", "id")}, 201)
	for _, u := range []string{"http://127.0.0.1:8080/x", "http://192.168.1.1/", "http://[::1]/", "http://169.254.169.254/latest", "http://100.100.100.100/"} {
		admin.do("POST", "/apps/ledger/webhooks", map[string]any{"url": u}, 400)
	}

	// Names are checked after DNS, when connecting.
	res, err := srv.webhookClient.Get("http://localhost:1/")
	if err == nil {
		res.Body.Close()
		t.Fatal("webhook client connected to localhost")
	}
	if !strings.Contains(err.Error(), "private or local address") {
		t.Fatalf("err = %v", err)
	}
}

func TestOutboundAllowed(t *testing.T) {
	allowed := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}
	for addr, want := range map[string]bool{
		"1.1.1.1":            true,
		"2606:4700::1111":    true,
		"10.1.2.3":           true, // allowed explicitly
		"10.2.0.1":           false,
		"127.0.0.1":          false,
		"172.16.0.1":         false,
		"192.168.0.10":       false,
		"169.254.169.254":    false,
		"100.64.0.1":         false,
		"0.0.0.0":            false,
		"::1":                false,
		"fd00::1":            false,
		"fe80::1":            false,
		"::ffff:127.0.0.1":   false,
		"::ffff:192.168.1.1": false,
		"64:ff9b::a00:1":     false,
		"224.0.0.1":          false,
	} {
		if got := outboundAllowed(netip.MustParseAddr(addr), allowed); got != want {
			t.Errorf("outboundAllowed(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestProxyClientIP(t *testing.T) {
	h := http.Header{}
	h.Add("X-Forwarded-For", "6.6.6.6, 203.0.113.7")
	if ip := proxyClientIP(h); ip.String() != "203.0.113.7" {
		t.Fatalf("from X-Forwarded-For: %v", ip)
	}
	h.Set("X-Real-IP", "198.51.100.2")
	if ip := proxyClientIP(h); ip.String() != "198.51.100.2" {
		t.Fatalf("from X-Real-IP: %v", ip)
	}
}

func TestLimiterWindow(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	l.add("k")
	if l.retryAfter("k") != 0 {
		t.Fatal("blocked after one event")
	}
	l.add("k")
	if l.retryAfter("k") != time.Minute {
		t.Fatalf("retryAfter = %v", l.retryAfter("k"))
	}
	now = now.Add(time.Minute)
	if l.retryAfter("k") != 0 {
		t.Fatal("still blocked after the window")
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("headers = %v", res.Header)
	}
}
