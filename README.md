# arkserver-panel

Optionale, self-hosted Web-UI zum Verwalten des gehärteten ARK: Survival Evolved Servers
([`arkserver`](https://github.com/castrowithc/arkserver)). Komplett in Go, server-gerendert, ein
Admin-Login, LAN-only. Kein SaaS, ein Server pro Deployment, und nie verpflichtend: wer will,
bearbeitet die Dateien weiter direkt.

**Status:** v1. Am laufenden Server abgenommen.

## Was es kann
Die Seiten heißen wie die Bildschirme der Referenz-Oberfläche. Wo es kein Gegenstück gibt, ist die
Seite in der Navigation als eigene gekennzeichnet.

- **Status:** Zustand, Healthcheck, CPU, RAM gegen das Limit, Spielerliste, installierter
  Steam-Build. Aktualisiert sich selbst.
- **Lifecycle:** Neu starten (RCON, mit Speichern), Stoppen und Starten.
- **Basiseinstellungen und Engine Einstellungen:** erzeugtes Formular über 246 Felder der beiden
  INIs, aufgeteilt wie in der Referenz (210 und 36), je Seite gruppiert und filterbar; ein Speichern
  berührt nie ein Feld der anderen Seite. Ein leeres Feld heißt, dass der Key nicht in der Datei
  steht und der Wert des Spiels gilt, und bleibt beim Speichern auch weg; geschrieben wird nur, was
  geändert wurde. Zahlenfelder tragen
  Grenzen und Schrittweite, geprüft wird beim Speichern. 239 Felder nennen zusätzlich den Wert eines
  fremden Beispielservers als Größenordnung, ausdrücklich nicht als Vorgabewert. 229 der 246
  Felder sind durch eine laufende Installation belegt, 17 bisher nur durch ein Formular.
- **Konfigurationsdateien:** Roh-Editor für `GameUserSettings.ini` und `Game.ini`. Nach dem
  Speichern erinnert das Panel daran, dass die Änderung erst mit einem Neustart greift.
- **Logs:** read-only, jeweils das Ende der Datei.
- **`.env`:** read-only. Als Rohtext mit maskierten Passwörtern unter Konfigurationsdateien, dazu die
  Seite „Deployment": die Werte mit Namen und der Angabe, was jeder bewirkt und wo er geändert wird.
- **Was das Deployment besetzt, ist gesperrt:** neun Keys schreibt arkmanager bei jedem Start aus der
  `.env` in die `GameUserSettings.ini` (Servername, Passwörter, Slots, Mod-IDs, Ports). Das Formular
  zeigt sie mit Wert, aber schreibgeschützt und mit Begründung, statt eine Änderung anzunehmen, die
  der nächste Start verwirft.
- **Backup:** die vorhandenen Archive auflisten, herunterladen und eines zurückspielen. Das
  Zurückspielen stoppt den Server, ersetzt genau die Dateien aus dem Archiv (Welt-Stand,
  Spielerprofile und beide INIs) und startet ihn wieder.

Bewusst nicht drin: die Parameter prozeduraler Karten, schreibende `.env`, Mods-Browser, geplante
Neustarts. Sicherungen legt das Panel nicht selbst an: das erledigt der Server per Cron, vor jedem
Update und bei jedem Stop.

## Betrieb
Im Deployment steckt der Dienst im Compose-Profil `panel`, zusammen mit einem pfadgefilterten
Socket-Proxy, und ist standardmäßig aus:

```bash
docker compose --profile panel up -d panel socket-proxy
```

Einzeln, etwa zum Ausprobieren:

```bash
docker run --rm -p 127.0.0.1:8080:8080 \
  -e PANEL_ADDR=0.0.0.0:8080 -e PANEL_USER=admin -e PANEL_PASS=... \
  ghcr.io/castrowithc/arkserver-panel:0.1.1
```

Ohne Docker (Go 1.23+): `PANEL_USER=admin PANEL_PASS=... go run .`

## Konfiguration
Alles über Umgebungsvariablen (`.env.example`):

| Variable | Bedeutung |
|---|---|
| `PANEL_ADDR` | Bind-Adresse (Default `127.0.0.1:8080`, im Container `0.0.0.0:8080`). |
| `PANEL_USER` / `PANEL_PASS` | Admin-Credential (Basic Auth). **Pflicht**, sonst startet das Panel nicht. |
| `ARK_RCON_ADDR` / `ARK_ADMIN_PASSWORD` | RCON für Spielerliste und Neustart. |
| `ARK_DOCKER_HOST` / `ARK_CONTAINER` | Socket-Proxy und Containername für CPU, RAM, Start und Stopp. |
| `ARK_DATA_DIR` / `ARK_ENV_DIR` | Server-Volume und das Verzeichnis mit der `.env`. |

Fehlt eine der optionalen Quellen, blendet das Panel aus, was sie braucht, und schreibt den Grund auf
die Seite, statt den Dienst zu verweigern.

## Sicherheit
Basic Auth ist das Gate. Über reines HTTP wird das Credential nur base64-kodiert übertragen, daher
**nur im vertrauenswürdigen LAN** betreiben; Remote-Zugang über VPN. Der Docker-Socket wird nie ins
Panel gemountet: Zugriff läuft ausschließlich über den Proxy, der auf wenige Pfade des
Server-Containers begrenzt ist.

## Bauen und Veröffentlichen
CI baut das Image, prüft `go vet` und Tests, verlangt Digest-Pins für alle Basis-Images, scannt mit
Trivy (rot bei CRITICAL) und startet das gebaute Image zur Probe, bevor irgendetwas nach GHCR geht.
Ein Tag `vX.Y.Z` veröffentlicht `X.Y.Z`, `X.Y` und `latest` samt Release; `main` schiebt `edge`.

## Lizenz
[MIT](./LICENSE) © 2026 Christian Castro.
