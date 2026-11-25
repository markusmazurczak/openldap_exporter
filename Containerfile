# openldap-exporter: static Go binary on scratch (no shell, no package manager).
# BASE is pinned by digest; a rebuild on a newer base passes it via --build-arg BASE=...
ARG BASE=docker.io/library/golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d
FROM ${BASE} AS build
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
