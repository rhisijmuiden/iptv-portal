package web

import "strings"

// players maps parts of User-Agent headers to the app or device that sends
// them. The first match wins, so specific apps come before the generic
// libraries they are built on.
var players = []struct{ part, name string }{
	{"mytvonline", "Formuler / MyTVOnline"},
	{"formuler", "Formuler / MyTVOnline"},
	{"tivimate", "TiviMate"},
	{"smarters", "IPTV Smarters"},
	{"iptvx", "IPTVX"},
	{"kodi", "Kodi"},
	{"vlc", "VLC"},
	{"tizen", "Samsung TV"},
	{"web0s", "LG TV"},
	{"webos", "LG TV"},
	{"applecoremedia", "Apple device"},
	{"cfnetwork", "Apple device"},
	{"exoplayer", "Android app"},
	{"okhttp", "Android app"},
	{"dalvik", "Android app"},
	{"lavf", "FFmpeg-based player"},
	{"mozilla", "Web browser"},
}

// PlayerName guesses the app or device from a User-Agent header, in
// English; "Unknown player" when it cannot tell.
func PlayerName(userAgent string) string {
	ua := strings.ToLower(userAgent)
	for _, p := range players {
		if strings.Contains(ua, p.part) {
			return p.name
		}
	}
	return "Unknown player"
}
