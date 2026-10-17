# IPTV Portal

Een lichte portal om losse, publiek toegankelijke m3u8-streams (bijvoorbeeld van lokale omroepen) op één plek te verzamelen. Gebruikers stellen de portal in op hun tv-box of IPTV-app (zoals MyTVOnline op een Formuler) en krijgen zo alle kanalen en, waar beschikbaar, de programmagids.

De portal is alleen een index: tv-boxen maken direct verbinding met de stream van de omroep. Er loopt geen video via de server.

> **Status:** in ontwikkeling. Het beheerpaneel met kanaalbeheer werkt. Gebruikers en uitnodigingen, de Xtream Codes-API, M3U en EPG, de installer en de update-functie volgen.

## Onderdelen

| Onderdeel | Standaardpoort | Doel |
|---|---|---|
| Beheerpaneel | 8080 | Kanalen, categorieën, import en instellingen beheren |
| Portal | 80 | Het adres dat gebruikers op hun tv-box invullen |

Beide poorten, de naam van de portal en de publieke URL zijn te wijzigen onder **Instellingen** in het beheerpaneel. Na het wijzigen van een poort start dat onderdeel direct opnieuw op de nieuwe poort.

### Achter een reverse proxy

Draai je de portal achter een reverse proxy (Nginx, Caddy, Nginx Proxy Manager, Traefik), vul dan onder **Instellingen → Publieke URL** het adres van de proxy in, bijvoorbeeld `https://tv.example.com`. Alle links die naar tv-boxen gaan worden met dit adres opgebouwd.

## Zelf bouwen en draaien

Vereist Go 1.26 of nieuwer.

```sh
make build
IPTV_PORTAL_DATA=./data ./bin/iptv-portal serve
```

Bij de eerste start zonder admin-account wordt het account `admin` aangemaakt met een eenmalig wachtwoord. Dat wachtwoord staat in het logboek. Na het inloggen moet je het direct wijzigen.

## Opdrachten

```text
iptv-portal serve                         Start de portal
iptv-portal admin-reset [-username N]     Maak een admin aan of zet een nieuw eenmalig wachtwoord
iptv-portal set-port [-admin P] [-portal P]
                                          Wijzig de poorten vanaf de commandoregel
iptv-portal version                       Toon de versie
```

Alle opdrachten accepteren `-data DIR` (standaard `$IPTV_PORTAL_DATA` of `/var/lib/iptv-portal`).

Buitengesloten na het wijzigen van de poort van het beheerpaneel? Zet hem terug en herstart de service:

```sh
iptv-portal set-port -admin 8080
systemctl restart iptv-portal
```

## Kanalen importeren

Onder **Importeren** kun je een M3U-playlist uploaden, plakken of laten downloaden van een URL. De attributen `tvg-id` (EPG-koppeling), `tvg-logo` en `group-title` (categorie) worden overgenomen. Een kanaal met een stream-URL die al bestaat wordt bijgewerkt in plaats van dubbel toegevoegd.
