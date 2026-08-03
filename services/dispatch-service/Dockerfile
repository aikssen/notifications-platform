# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first, so a source change does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dispatcher ./cmd/dispatcher

# A static binary needs nothing but CA certificates, so the runtime image has
# no shell, no package manager and nothing else an attacker could pivot through.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/dispatcher /dispatcher
USER nonroot:nonroot
EXPOSE 9101
ENTRYPOINT ["/dispatcher"]
