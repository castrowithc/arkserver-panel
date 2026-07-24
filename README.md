# arkserver-panel

Optionale, self-hosted Web-UI zum Verwalten des gehärteten ARK: Survival Evolved Servers
([`arkserver`](https://github.com/castrowithc/arkserver)). Komplett in Go, server-gerendert, ein
Admin-Login, LAN-only. Kein SaaS, ein Server pro Deployment.

**Status:** v1 in Arbeit, Phase 1 (Skeleton). Noch keine Features, nur der abgesicherte Server-Rahmen.

## Quickstart
```bash
cp .env.example .env    # PANEL_USER / PANEL_PASS setzen
docker build -t arkserver-panel .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e PANEL_ADDR=0.0.0.0:8080 -e PANEL_USER=admin -e PANEL_PASS=... arkserver-panel
```
Ohne Docker (Go 1.23+): `PANEL_USER=admin PANEL_PASS=... go run .`

## Konfiguration
Alles über Umgebungsvariablen (`.env.example`):
- `PANEL_ADDR` — Bind-Adresse (Default `127.0.0.1:8080`; im Container `0.0.0.0:8080`).
- `PANEL_USER` / `PANEL_PASS` — Admin-Credential (Basic Auth), Pflicht.

## Sicherheit
Basic Auth ist das Gate. Über reines HTTP wird das Credential nur base64-kodiert übertragen, daher
**nur im vertrauenswürdigen LAN** betreiben; Remote-Zugang über VPN. Internet-Exposition/HTTPS kommt
als eigener späterer Schritt.

## Steuerung & Doku
Planung, Backlog und Design liegen im Schwester-Repo
[`arkserver-ops`](https://github.com/castrowithc/arkserver-ops) (`docs/panel/`, Backlog-Projekt `panel`).

## Lizenz
[MIT](./LICENSE) © 2026 Christian Castro.
