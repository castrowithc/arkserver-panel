# Build a static binary, ship it on a minimal nonroot base.
# Both bases are pinned by digest (index digest, so every architecture still resolves) and the CI
# fails if any FROM here loses its pin. Renew a pin deliberately, never by following a tag.
# Der Builder muss auf einer noch gepflegten Go-Linie stehen: die statische Binary traegt ihre
# stdlib in sich, eine ausgelaufene Linie bekommt keine Fixes mehr und der Trivy-Gate wird rot
# (1.23 lief so in CVE-2025-68121, crypto/tls).
FROM golang:1.25-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /panel .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35
COPY --from=build /panel /panel
# The app binds PANEL_ADDR (default 127.0.0.1:8080). In a container set PANEL_ADDR=0.0.0.0:8080
# and publish the port only to your trusted LAN. Basic Auth (PANEL_USER/PANEL_PASS) is required.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/panel"]
