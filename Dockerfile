# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/kmpoltorak/dns-consistency-checker/internal/cli.Version=${VERSION}" \
      -o /out/dns-consistency-checker ./cmd/dns-consistency-checker

# Static binary on distroless: no shell, no compiler, non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/dns-consistency-checker /usr/local/bin/dns-consistency-checker
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dns-consistency-checker"]
CMD ["help"]
