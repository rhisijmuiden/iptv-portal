package guides

// Default is a guide that comes with the portal. New installations start
// with these; the admin can change, hide or remove them and add them back.
type Default struct {
	Key    string // stays the same, so a removed guide can be added back
	Icon   string
	Name   string
	Body   string
	NameNL string
	BodyNL string
}

// Icons are the icons a guide can show.
var Icons = []string{"tv", "box", "smartphone", "laptop"}

// Defaults are the guides that come with the portal.
var Defaults = []Default{
	{
		Key: "mytvonline", Icon: "box",
		Name: "Formuler with MyTVOnline",
		Body: `For Formuler boxes such as the Z11 Pro and Z12 Ultra, with the MyTVOnline 2, 3 or V4 app.

1. Open **MyTVOnline** on your Formuler.
2. Choose **Add portal** (on later versions: the menu, then **Portal settings** and **Add portal**).
3. Choose **Xtream Codes** (in some versions **XC**) as the login type.
4. Enter a portal name you like, for example ` + "`{portal}`" + `.
5. Enter the server address: ` + "`{server}`" + `
6. Enter your username ` + "`{username}`" + ` and your password.
7. Choose **Connect** or **Save**. The channels, radio stations and TV guide are loaded; the first time this can take a minute.

> Radio stations are listed after the TV channels, in their own categories.

## Did it not work?
- Check that the server address starts with ` + "`http://` or `https://`" + ` exactly as shown above.
- Your password is the one you log in with on this page. Changed it? Enter the new one in MyTVOnline as well.
- Restart the box and open the portal again.`,
		NameNL: "Formuler met MyTVOnline",
		BodyNL: `Voor Formuler-boxen zoals de Z11 Pro en Z12 Ultra, met de app MyTVOnline 2, 3 of V4.

1. Open **MyTVOnline** op je Formuler.
2. Kies **Add portal** (in nieuwere versies: het menu, dan **Portal settings** en **Add portal**).
3. Kies **Xtream Codes** (in sommige versies **XC**) als manier van inloggen.
4. Vul een portalnaam naar keuze in, bijvoorbeeld ` + "`{portal}`" + `.
5. Vul het serveradres in: ` + "`{server}`" + `
6. Vul je gebruikersnaam ` + "`{username}`" + ` en je wachtwoord in.
7. Kies **Connect** of **Save**. De zenders, radiozenders en tv-gids worden geladen; de eerste keer kan dat een minuut duren.

> Radiozenders staan na de tv-zenders, in hun eigen categorieën.

## Werkt het niet?
- Controleer dat het serveradres begint met ` + "`http://` of `https://`" + `, precies zoals hierboven.
- Je wachtwoord is het wachtwoord waarmee je op deze pagina inlogt. Heb je het gewijzigd? Vul het nieuwe dan ook in MyTVOnline in.
- Herstart de box en open de portal opnieuw.`,
	},
	{
		Key: "tivimate", Icon: "tv",
		Name: "TiviMate (Android TV, Google TV, Fire TV)",
		Body: `TiviMate is a popular app for Android TV boxes, TVs with Google TV and Amazon Fire TV sticks.

1. Install **TiviMate IPTV Player** from the Google Play Store or the Amazon Appstore.
2. Open the app and choose **Add playlist**.
3. Choose **Xtream Codes**.
4. Enter the server address ` + "`{server}`" + `, your username ` + "`{username}`" + ` and your password, then choose **Next**.
5. Give the playlist a name, for example ` + "`{portal}`" + `, and choose **Done**.

> The TV guide comes with the Xtream Codes login, so you do not have to add it separately.`,
		NameNL: "TiviMate (Android TV, Google TV, Fire TV)",
		BodyNL: `TiviMate is een populaire app voor Android TV-boxen, tv's met Google TV en Amazon Fire TV-sticks.

1. Installeer **TiviMate IPTV Player** uit de Google Play Store of de Amazon Appstore.
2. Open de app en kies **Add playlist** (Afspeellijst toevoegen).
3. Kies **Xtream Codes**.
4. Vul het serveradres ` + "`{server}`" + `, je gebruikersnaam ` + "`{username}`" + ` en je wachtwoord in en kies **Next**.
5. Geef de afspeellijst een naam, bijvoorbeeld ` + "`{portal}`" + `, en kies **Done**.

> De tv-gids komt mee met de Xtream Codes-login; je hoeft hem niet apart toe te voegen.`,
	},
	{
		Key: "smarters", Icon: "tv",
		Name: "Smart TV (Samsung, LG) and IPTV Smarters",
		Body: `IPTV Smarters Pro works on many smart TVs (Samsung, LG), Android TV, Fire TV, phones and tablets. The menus are almost the same everywhere.

1. Install **IPTV Smarters Pro** from the app store of your TV or device. On a Samsung or LG TV, search the app store for "IPTV Smarters".
2. Open the app and choose **Login with Xtream Codes API**.
3. Enter a name you like, for example ` + "`{portal}`" + `.
4. Enter your username ` + "`{username}`" + ` and your password.
5. Enter the URL: ` + "`{server}`" + `
6. Choose **Add user**. The app loads the channels and the TV guide.

> Does your TV app only accept a playlist link (M3U)? Use the M3U playlist link on the page "Set up your TV" instead.`,
		NameNL: "Smart-tv (Samsung, LG) en IPTV Smarters",
		BodyNL: `IPTV Smarters Pro werkt op veel smart-tv's (Samsung, LG), Android TV, Fire TV, telefoons en tablets. De menu's zijn overal bijna hetzelfde.

1. Installeer **IPTV Smarters Pro** uit de appwinkel van je tv of apparaat. Zoek op een Samsung- of LG-tv in de appwinkel naar "IPTV Smarters".
2. Open de app en kies **Login with Xtream Codes API**.
3. Vul een naam naar keuze in, bijvoorbeeld ` + "`{portal}`" + `.
4. Vul je gebruikersnaam ` + "`{username}`" + ` en je wachtwoord in.
5. Vul de URL in: ` + "`{server}`" + `
6. Kies **Add user**. De app laadt de zenders en de tv-gids.

> Accepteert de app op je tv alleen een afspeellijstlink (M3U)? Gebruik dan de M3U-afspeellijst op de pagina "Je tv instellen".`,
	},
	{
		Key: "apple", Icon: "smartphone",
		Name: "Apple TV, iPhone and iPad",
		Body: `On Apple devices you need an IPTV app from the App Store that supports Xtream Codes, such as IPTV Smarters Pro, IPTVX or UHF.

1. Install the app from the App Store.
2. Add a new playlist or provider and choose **Xtream Codes** (sometimes called **Xtream** or **XC API**).
3. Enter the server address ` + "`{server}`" + `, your username ` + "`{username}`" + ` and your password.
4. Save. The channels and the TV guide are loaded.`,
		NameNL: "Apple TV, iPhone en iPad",
		BodyNL: `Op Apple-apparaten heb je een IPTV-app uit de App Store nodig die Xtream Codes ondersteunt, zoals IPTV Smarters Pro, IPTVX of UHF.

1. Installeer de app uit de App Store.
2. Voeg een nieuwe afspeellijst of provider toe en kies **Xtream Codes** (soms **Xtream** of **XC API** genoemd).
3. Vul het serveradres ` + "`{server}`" + `, je gebruikersnaam ` + "`{username}`" + ` en je wachtwoord in.
4. Sla op. De zenders en de tv-gids worden geladen.`,
	},
	{
		Key: "computer", Icon: "laptop",
		Name: "Computer with VLC or Kodi",
		Body: `On a Windows, Mac or Linux computer you can watch with VLC or Kodi, using the M3U playlist link. Copy the link from the page "Set up your TV", with your password filled in.

## VLC
1. Install VLC from [videolan.org](https://www.videolan.org).
2. Choose **Media** and then **Open Network Stream** (on a Mac: **File** and **Open Network**).
3. Paste your M3U playlist link and choose **Play**. Open the playlist (Ctrl+L) to pick a channel.

## Kodi
1. In Kodi, go to **Add-ons**, **Install from repository**, **PVR clients** and install **PVR IPTV Simple Client**.
2. Open the settings of the add-on and paste your M3U playlist link under **M3U playlist URL**.
3. Paste the program guide link under **XMLTV URL** for the TV guide.
4. Restart Kodi. The channels are under **TV**.`,
		NameNL: "Computer met VLC of Kodi",
		BodyNL: `Op een Windows-, Mac- of Linux-computer kun je kijken met VLC of Kodi, met de M3U-afspeellijstlink. Kopieer de link van de pagina "Je tv instellen", met je wachtwoord ingevuld.

## VLC
1. Installeer VLC via [videolan.org](https://www.videolan.org).
2. Kies **Media** en dan **Netwerkstroom openen** (op een Mac: **Archief** en **Open netwerk**).
3. Plak je M3U-afspeellijstlink en kies **Afspelen**. Open de afspeellijst (Ctrl+L) om een zender te kiezen.

## Kodi
1. Ga in Kodi naar **Add-ons**, **Installeren via repository**, **PVR-clients** en installeer **PVR IPTV Simple Client**.
2. Open de instellingen van de add-on en plak je M3U-afspeellijstlink bij **M3U playlist URL**.
3. Plak de link van de programmagids bij **XMLTV URL** voor de tv-gids.
4. Herstart Kodi. De zenders staan onder **TV**.`,
	},
}
