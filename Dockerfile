# Build a static binary, ship it on a minimal nonroot base.
# Base images are pinned by digest in Phase 4 (CI); floating tags are fine for the skeleton.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /panel .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /panel /panel
# The app binds PANEL_ADDR (default 127.0.0.1:8080). In a container set PANEL_ADDR=0.0.0.0:8080
# and publish the port only to your trusted LAN. Basic Auth (PANEL_USER/PANEL_PASS) is required.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/panel"]
