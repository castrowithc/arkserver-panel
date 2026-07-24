# AGENTS.md — arkserver-panel (Code)

Schlankes **Code-Repo** für das optionale Management-Panel des ARK-SE-Servers. Komplett in Go,
server-gerendert (`net/http` + `html/template` + htmx), eine statische Binary, ein Admin-Login,
LAN-only. Kein SaaS.

## Steuerung & Doku liegen woanders
Planung, Backlog, Design und AI-Kontext leben im Schwester-Repo **`../arkserver-ops`**
(`docs/panel/`, Backlog-Projekt `panel`). Vor inhaltlicher Arbeit dort `v1-plan.md` und die aktive
Phase im Panel-Roster lesen.

## Konventionen
- **Eine Session = eine Backlog-Phase.**
- Standardbibliothek zuerst, Dependencies nur wenn nötig (YAGNI).
- Secrets nur in `.env` (gitignored), niemals committen.
- Basis-Images in der CI per Digest gepinnt — keine schwebenden `latest`-Abhängigkeiten in Produktion.
