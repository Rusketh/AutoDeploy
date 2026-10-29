package portal

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The settings hub (/portal/settings): one card per settings page whose
// headline is that area's live state ("PIN required", "HTTPS on · cert
// expires in 6 days") and which turns amber or red, with a short reason,
// when something needs attention. Everything here must be cheap — no
// network calls on page load: checks that do talk to the network (the AD
// connection test, the GitHub release check) are read from what their own
// pages last found.

// hubCard is one card on the hub.
type hubCard struct {
	Href, Icon, Title string
	Value             string // headline: the current state
	Detail            string // what the page configures
	Tone              string // "" | ok | warn | bad
	Reason            string // why it needs attention (Tone warn/bad)
}

// hubState is everything the cards are derived from, gathered cheaply.
type hubState struct {
	Now time.Time

	PINOn bool

	PortalNotify    bool
	EmailOn         bool
	SMTPHost        string
	WebhooksEnabled int

	ProductName string

	Users, DisabledUsers int
	BootstrapPresent     bool

	ADURL      string
	ADTest     *adTestResult // last connection test of ADURL, if any
	HTTPAddr   string
	HTTPSAddr  string
	ExtURL     string
	CertPath   string
	CertExpiry time.Time // zero when unknown
	CertErr    string

	RetentionDays  int
	Timezone       string
	CheckInSeconds int

	StorageOverrides int
	StorageProblems  []string // "iso: directory not writable"
	FreeBytes        uint64
	TotalBytes       uint64
	FreeKnown        bool

	ServerVersion string
	LatestVersion string // from the Updates page's last check ("" = not checked)
	AgentVersion  string // staged windows/amd64 agent ("" = none staged)
}

// adTestResult is the outcome of the last "Test connection" on the AD page.
type adTestResult struct {
	URL string
	OK  bool
	Err string
	At  time.Time
}

// Results of checks the hub may not repeat itself (they dial out). Kept
// in memory: a restart just means "not checked yet".
var hubChecks struct {
	sync.Mutex
	ad            *adTestResult
	latest        string
	latestChecked time.Time
}

func rememberADTest(res adTestResult) {
	hubChecks.Lock()
	defer hubChecks.Unlock()
	hubChecks.ad = &res
}

// forgetADTest drops the last result after the AD settings change: it
// tested credentials that may no longer be the ones in use.
func forgetADTest() {
	hubChecks.Lock()
	defer hubChecks.Unlock()
	hubChecks.ad = nil
}

func lastADTest(url string) *adTestResult {
	hubChecks.Lock()
	defer hubChecks.Unlock()
	if hubChecks.ad == nil || hubChecks.ad.URL != url {
		return nil
	}
	res := *hubChecks.ad
	return &res
}

func rememberLatestRelease(version string) {
	hubChecks.Lock()
	defer hubChecks.Unlock()
	hubChecks.latest, hubChecks.latestChecked = version, time.Now()
}

// lastLatestRelease is the newest release the Updates page saw in the last
// day ("" when it hasn't looked, or looked too long ago).
func lastLatestRelease(now time.Time) string {
	hubChecks.Lock()
	defer hubChecks.Unlock()
	if now.Sub(hubChecks.latestChecked) > 24*time.Hour {
		return ""
	}
	return hubChecks.latest
}

// certExpiry reads a PEM certificate's expiry.
func certExpiry(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return time.Time{}, fmt.Errorf("not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

// gatherHubState reads the hub's inputs: settings already cached in
// memory, a couple of small queries, a few stats and file reads.
func gatherHubState(req *http.Request, r Repos) hubState {
	ctx := req.Context()
	st := hubState{Now: time.Now(), ServerVersion: serverVersionFor(r)}
	if r.Settings != nil {
		st.PINOn, _ = r.Settings.AccessPINEnabled(ctx)
	}
	if r.Branding != nil {
		b, _ := r.Branding.Get(ctx)
		st.ProductName = b.ProductName
	}
	if r.Users != nil {
		users, _ := r.Users.ListUsers(ctx)
		st.Users = len(users)
		for _, u := range users {
			if u.Disabled {
				st.DisabledUsers++
			}
		}
	}
	if r.DataDir != "" {
		if _, err := os.Stat(filepath.Join(r.DataDir, "admin-bootstrap.txt")); err == nil {
			st.BootstrapPresent = true
		}
	}
	if r.WebhookRepo != nil {
		hooks, _ := r.WebhookRepo.List(ctx)
		for _, h := range hooks {
			if h.Enabled {
				st.WebhooksEnabled++
			}
		}
	}
	if r.Runtime != nil {
		st.PortalNotify = r.Runtime.NotifyPortalEnabled()
		st.EmailOn = r.Runtime.NotifyEmailEnabled()
		st.SMTPHost = r.Runtime.NotifyEmailConfig(ctx).Host
		st.ADURL = r.Runtime.ADConfig(ctx).URL
		net := r.Runtime.NetworkConfig()
		env := r.Runtime.EnvDefaults()
		st.HTTPAddr, st.HTTPSAddr, st.ExtURL = firstNonEmpty(net.HTTPAddr, env.HTTPAddr), firstNonEmpty(net.HTTPSAddr, env.HTTPSAddr), net.ExternalURL
		st.CertPath = firstNonEmpty(net.TLSCertPath, env.TLSCertFile)
		st.RetentionDays = r.Runtime.LogRetentionDays()
		st.Timezone = r.Runtime.DisplayTimezone()
		st.CheckInSeconds = r.Runtime.AgentPollIntervalSeconds()
	}
	if st.ADURL != "" {
		st.ADTest = lastADTest(st.ADURL)
	}
	if st.HTTPSAddr != "" && st.CertPath != "" {
		if exp, err := certExpiry(st.CertPath); err != nil {
			st.CertErr = err.Error()
		} else {
			st.CertExpiry = exp
		}
	}
	for _, e := range collectStorageEntries(r) {
		if e.Override != "" {
			st.StorageOverrides++
		}
		// A missing default directory is created on first use; a missing
		// override, or anything unwritable, is a real problem.
		if e.State == "unreadable" || (e.State == "missing" && e.Override != "") {
			st.StorageProblems = append(st.StorageProblems, e.Category+": "+e.Detail)
		}
	}
	st.FreeBytes, st.TotalBytes, st.FreeKnown = diskFree(dataDirFor(r))
	st.LatestVersion = lastLatestRelease(st.Now)
	if dir := downloadsDir(r); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, agentFilename("windows", "amd64")+".version")); err == nil {
			st.AgentVersion = strings.TrimSpace(string(b))
		}
	}
	return st
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// lowDiskBytes is the free space under which the storage card warns:
// roughly one Windows ISO plus an extracted image.
const lowDiskBytes = 10 << 30

// certWarnWithin is how close to expiry the network card turns amber.
const certWarnWithin = 14 * 24 * time.Hour

// hubCards derives the cards from the gathered state.
func hubCards(st hubState) []hubCard {
	var cards []hubCard
	add := func(c hubCard) { cards = append(cards, c) }

	// Access PIN.
	pin := hubCard{Href: "/portal/settings/access-pin", Icon: "i-shield", Title: "Access PIN",
		Value: "PIN off", Detail: "Any machine that network-boots can open the deploy menu."}
	if st.PINOn {
		pin.Value, pin.Tone, pin.Detail = "PIN required", "ok", "Machines must enter the PIN before the deploy menu opens."
	}
	add(pin)

	// Notifications.
	channels := 0
	for _, on := range []bool{st.PortalNotify, st.EmailOn, st.WebhooksEnabled > 0} {
		if on {
			channels++
		}
	}
	notif := hubCard{Href: "/portal/settings/notifications", Icon: "i-bell", Title: "Notifications",
		Detail: "In-portal alerts, email and webhooks."}
	notif.Value = fmt.Sprintf("%d channel%s on", channels, plural(channels))
	switch {
	case st.EmailOn && st.SMTPHost == "":
		notif.Value += " · SMTP not configured"
		notif.Tone, notif.Reason = "warn", "Email is switched on but no mail server is set, so no email is sent."
	case st.SMTPHost == "":
		notif.Value += " · SMTP not configured"
	}
	add(notif)

	// Branding.
	brand := st.ProductName
	if brand == "" {
		brand = "Default"
	}
	add(hubCard{Href: "/portal/settings/branding", Icon: "i-image", Title: "Branding",
		Value: brand, Detail: "Portal name, logo and colour; the OEM details written to deployed machines."})

	// Accounts.
	acc := hubCard{Href: "/portal/settings/accounts", Icon: "i-users", Title: "Local accounts",
		Value: fmt.Sprintf("%d user%s", st.Users, plural(st.Users)), Detail: "People who can sign in to this portal."}
	if st.DisabledUsers > 0 {
		acc.Value += fmt.Sprintf(" · %d disabled", st.DisabledUsers)
	}
	if st.BootstrapPresent {
		acc.Tone, acc.Reason = "bad", "admin-bootstrap.txt (the first admin password, in plain text) is still on disk — delete it."
	}
	add(acc)

	// Active Directory.
	ad := hubCard{Href: "/portal/settings/active-directory", Icon: "i-globe", Title: "Active Directory",
		Value: "AD: not configured", Detail: "Creates and cleans up computer accounts for domain-joined deploys."}
	if st.ADURL != "" {
		ad.Value = "AD: configured · not tested"
		if t := st.ADTest; t != nil && t.OK {
			ad.Value, ad.Tone = "AD: connected", "ok"
			ad.Detail = "Connection test passed " + relativeTime(t.At, st.Now, time.UTC) + "."
		} else if t != nil {
			ad.Value, ad.Tone = "AD: connection failing", "bad"
			ad.Reason = "The last connection test failed: " + t.Err
		}
	}
	add(ad)

	// Network.
	netc := hubCard{Href: "/portal/settings/network", Icon: "i-globe", Title: "Network",
		Detail: "Listen addresses, HTTPS certificate, reverse proxy and external URL."}
	switch {
	case st.HTTPSAddr == "":
		netc.Value = "HTTP only"
	case st.CertPath == "":
		netc.Value = "HTTPS on · self-signed certificate"
	case st.CertErr != "":
		netc.Value, netc.Tone = "HTTPS on · certificate unreadable", "bad"
		netc.Reason = "Can't read the certificate: " + st.CertErr
	default:
		left := st.CertExpiry.Sub(st.Now)
		switch {
		case left <= 0:
			netc.Value, netc.Tone = "HTTPS on · certificate expired", "bad"
			netc.Reason = "The certificate expired " + st.CertExpiry.Format("2 Jan 2006") + ". Browsers and agents will refuse the connection."
		case left < certWarnWithin:
			days := int(left.Hours() / 24)
			netc.Value = fmt.Sprintf("HTTPS on · cert expires in %d day%s", days, plural(days))
			if days == 0 {
				netc.Value = "HTTPS on · cert expires today"
			}
			netc.Tone, netc.Reason = "warn", "Renew the certificate before "+st.CertExpiry.Format("2 Jan 2006")+"."
		default:
			netc.Value = "HTTPS on · cert valid until " + st.CertExpiry.Format("2 Jan 2006")
		}
	}
	if st.ExtURL != "" && netc.Tone == "" {
		netc.Detail = "Reachable from outside at " + st.ExtURL + "."
	}
	add(netc)

	// Operational.
	ops := hubCard{Href: "/portal/settings/operational", Icon: "i-gear", Title: "Operational"}
	if st.RetentionDays > 0 {
		ops.Value = fmt.Sprintf("Logs kept %d day%s", st.RetentionDays, plural(st.RetentionDays))
	} else {
		ops.Value = "Logs kept forever"
	}
	tz := st.Timezone
	if tz == "" {
		tz = "UTC"
	}
	ops.Detail = "Times shown in " + tz + " · agents check in every " + checkInText(st.CheckInSeconds) + "."
	add(ops)

	// Storage.
	stor := hubCard{Href: "/portal/settings/storage", Icon: "i-box", Title: "Storage",
		Value: "Default locations", Detail: "Where ISOs, drivers, software and downloads are kept."}
	if st.StorageOverrides > 0 {
		stor.Value = fmt.Sprintf("%d custom location%s", st.StorageOverrides, plural(st.StorageOverrides))
	}
	if st.FreeKnown {
		stor.Value += " · " + humanBytes(int64(st.FreeBytes)) + " free"
	}
	switch {
	case len(st.StorageProblems) > 0:
		stor.Tone, stor.Reason = "bad", strings.Join(st.StorageProblems, "; ")
	case st.FreeKnown && st.FreeBytes < lowDiskBytes:
		stor.Tone, stor.Reason = "warn", "Low disk space: "+humanBytes(int64(st.FreeBytes))+" left in the data directory."
	}
	add(stor)

	// Downloads and PXE: static pages with nothing live worth a lookup.
	add(hubCard{Href: "/portal/downloads", Icon: "i-download", Title: "Downloads",
		Value: "Agent & tools", Detail: "Windows agent, Boot Client and installers to download."})

	// Updates.
	upd := hubCard{Href: "/portal/settings/updates", Icon: "i-refresh", Title: "Updates",
		Detail: "Server and agent versions; check for a newer release."}
	upd.Value = "Server " + st.ServerVersion
	if st.AgentVersion != "" {
		upd.Value += " · Agent " + st.AgentVersion
	} else {
		upd.Value += " · no agent staged"
	}
	if st.LatestVersion != "" && computeUpdateState(st.ServerVersion, st.LatestVersion) == "older" {
		upd.Value += " · update available"
		upd.Tone, upd.Reason = "warn", st.LatestVersion+" is available."
	}
	add(upd)

	add(hubCard{Href: "/portal/settings/pxe", Icon: "i-bolt", Title: "PXE setup",
		Value: "iPXE & DHCP", Detail: "Boot files this server serves, and DHCP settings to copy into your router or DHCP server."})
	return cards
}

// checkInText renders the agent poll interval: "5 min", "30 s", "2 h".
func checkInText(sec int) string {
	switch {
	case sec <= 0:
		return "5 min"
	case sec%3600 == 0:
		return fmt.Sprintf("%d h", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%d min", sec/60)
	}
	return fmt.Sprintf("%d s", sec)
}
