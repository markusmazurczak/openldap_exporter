FROM golang:1.22-alpine AS builder

ARG GIT_TAG=dev
ARG GIT_COMMIT=unknown

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.tag=${GIT_TAG} -X main.commit=${GIT_COMMIT}" \
    -o openldap_exporter main.go

FROM alpine:3.19

RUN apk add --no-cache ca-certificates

COPY --from=builder /build/openldap_exporter /openldap_exporter

EXPOSE 9330

USER nobody

ENTRYPOINT ["/openldap_exporter"]
