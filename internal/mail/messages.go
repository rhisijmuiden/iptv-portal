package mail

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
)

// Login holds what a user needs to set up their player.
type Login struct {
	PortalName string
	PortalURL  string // the address entered in the player, without trailing /
	Username   string
	Password   string
	ExpiresAt  *time.Time // nil = never
	// AccountURL is the user's account page; empty leaves it out.
	AccountURL string
}

// LoginMessage is the email, in language lang, that hands a user their
// login details.
func LoginMessage(lang, to string, l Login) Message {
	t := func(s string) string { return i18n.T(lang, s) }
	q := "username=" + url.QueryEscape(l.Username) + "&password=" + url.QueryEscape(l.Password)
	var b strings.Builder
	fmt.Fprintf(&b, t("Hello,\n\nYour account for %s is ready. Add the portal to your TV box or IPTV app with these details.\n\n"), l.PortalName)
	b.WriteString(t("XTREAM CODES LOGIN (recommended)\n"))
	b.WriteString(t("For MyTVOnline (Formuler), TiviMate, IPTV Smarters and most other apps.\n\n"))
	fmt.Fprintf(&b, t("  Server / portal URL: %s\n"), l.PortalURL)
	fmt.Fprintf(&b, t("  Username:            %s\n"), l.Username)
	fmt.Fprintf(&b, t("  Password:            %s\n\n"), l.Password)
	b.WriteString(t("On a Formuler with MyTVOnline: Add portal > choose Xtream Codes (or XC) > enter the details above.\n\n"))
	b.WriteString(t("M3U PLAYLIST\nFor apps that only accept a playlist link:\n\n"))
	fmt.Fprintf(&b, "  %s/get.php?%s&type=m3u_plus&output=m3u8\n\n", l.PortalURL, q)
	b.WriteString(t("PROGRAM GUIDE (EPG)\n\n"))
	fmt.Fprintf(&b, "  %s/xmltv.php?%s\n\n", l.PortalURL, q)
	if l.ExpiresAt != nil {
		fmt.Fprintf(&b, t("Your account is valid until %s.\n\n"), i18n.LongDate(lang, l.ExpiresAt.Local()))
	}
	if l.AccountURL != "" {
		fmt.Fprintf(&b, t("See your account, change your password or reset a forgotten one at:\n\n  %s\n\n"), l.AccountURL)
	}
	b.WriteString(t("Keep this email private: anyone with these details can use your account.\n"))
	return Message{To: to, Subject: i18n.Tf(lang, "Your login details for %s", l.PortalName), Body: b.String()}
}

// PasswordResetMessage, in language lang, sends a user the link to choose
// a new password.
func PasswordResetMessage(lang, to, portalName, username, link string, valid time.Duration) Message {
	body := i18n.Tf(lang, "Hello,\n\nSomeone asked to reset the password of your account %s at %s.\n\nOpen this link to choose a new password:\n\n  %s\n\nThe link works once and for %d minutes. Did you not ask for this? Then ignore this email: your password stays the same.\n",
		username, portalName, link, int(valid.Minutes()))
	return Message{To: to, Subject: i18n.Tf(lang, "Reset your password for %s", portalName), Body: body}
}

// PasswordChangedMessage, in language lang, tells a user their password
// was changed, so they notice when it was not them.
func PasswordChangedMessage(lang, to, portalName, username, accountURL string) Message {
	body := i18n.Tf(lang, "Hello,\n\nThe password of your account %s at %s has just been changed. Enter the new password in your TV box or IPTV app as well.\n\nWas this not you? Reset your password right away at:\n\n  %s\n",
		username, portalName, accountURL)
	return Message{To: to, Subject: i18n.Tf(lang, "Your password for %s was changed", portalName), Body: body}
}

// InviteMessage is the email, in language lang, that sends someone an
// invitation link.
func InviteMessage(lang, to, portalName, link string) Message {
	body := i18n.Tf(lang, "Hello,\n\nYou have been invited to %s.\n\nOpen this link to choose your own username and password:\n\n  %s\n\nAfter signing up you get the details to set up your TV box or IPTV app.\n",
		portalName, link)
	return Message{To: to, Subject: i18n.Tf(lang, "Your invitation to %s", portalName), Body: body}
}

// TestMessage, in language lang, checks the mail settings.
func TestMessage(lang, to, portalName string) Message {
	return Message{To: to, Subject: i18n.Tf(lang, "%s test email", portalName),
		Body: i18n.Tf(lang, "This is a test email from %s.\n\nYour mail server settings work.\n", portalName)}
}
