package mail

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Login holds what a user needs to set up their player.
type Login struct {
	PortalName string
	PortalURL  string // the address entered in the player, without trailing /
	Username   string
	Password   string
	ExpiresAt  *time.Time // nil = never
}

// LoginMessage is the email that hands a user their login details.
func LoginMessage(to string, l Login) Message {
	q := "username=" + url.QueryEscape(l.Username) + "&password=" + url.QueryEscape(l.Password)
	var b strings.Builder
	fmt.Fprintf(&b, "Hello,\n\nYour account for %s is ready. Add the portal to your TV box or IPTV app with these details.\n\n", l.PortalName)
	b.WriteString("XTREAM CODES LOGIN (recommended)\n")
	b.WriteString("For MyTVOnline (Formuler), TiviMate, IPTV Smarters and most other apps.\n\n")
	fmt.Fprintf(&b, "  Server / portal URL: %s\n", l.PortalURL)
	fmt.Fprintf(&b, "  Username:            %s\n", l.Username)
	fmt.Fprintf(&b, "  Password:            %s\n\n", l.Password)
	b.WriteString("On a Formuler with MyTVOnline: Add portal > choose Xtream Codes (or XC) > enter the details above.\n\n")
	b.WriteString("M3U PLAYLIST\nFor apps that only accept a playlist link:\n\n")
	fmt.Fprintf(&b, "  %s/get.php?%s&type=m3u_plus&output=m3u8\n\n", l.PortalURL, q)
	b.WriteString("PROGRAM GUIDE (EPG)\n\n")
	fmt.Fprintf(&b, "  %s/xmltv.php?%s\n\n", l.PortalURL, q)
	if l.ExpiresAt != nil {
		fmt.Fprintf(&b, "Your account is valid until %s.\n\n", l.ExpiresAt.Local().Format("2 January 2006"))
	}
	b.WriteString("Keep this email private: anyone with these details can use your account.\n")
	return Message{To: to, Subject: "Your login details for " + l.PortalName, Body: b.String()}
}

// InviteMessage is the email that sends someone an invitation link.
func InviteMessage(to, portalName, link string) Message {
	body := fmt.Sprintf("Hello,\n\nYou have been invited to %s.\n\n"+
		"Open this link to choose your own username and password:\n\n  %s\n\n"+
		"After signing up you get the details to set up your TV box or IPTV app.\n", portalName, link)
	return Message{To: to, Subject: "Your invitation to " + portalName, Body: body}
}

// TestMessage checks the mail settings.
func TestMessage(to, portalName string) Message {
	return Message{To: to, Subject: portalName + " test email",
		Body: "This is a test email from " + portalName + ".\n\nYour mail server settings work.\n"}
}
