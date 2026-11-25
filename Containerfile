# openldap-exporter: static Go binary on scratch (no shell, no package manager).
# The builder is pinned by tag and digest; Dependabot proposes updates (.github/dependabot.yml).
FROM docker.io/library/golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY *.go ./
# A build with failing tests never becomes an image.
RUN CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
ARG GIT_TAG=dev
ARG GIT_COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -buildid= -X main.tag=${GIT_TAG} -X main.commit=${GIT_COMMIT}" \
    -o /out/openldap-exporter .

FROM scratch
# CA bundle for ldaps:// targets
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/openldap-exporter /openldap-exporter
USER 65534:65534
EXPOSE 9330
ENTRYPOINT ["/openldap-exporter"]
