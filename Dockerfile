FROM golang:1.22-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/aics ./cmd/aics

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/aics /app/aics
COPY configs /app/configs
COPY skills /app/skills

ENV HTTP_ADDR=:8080
EXPOSE 8080

ENTRYPOINT ["/app/aics"]
