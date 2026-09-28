FROM golang:1.27 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /main ./cmd/main

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /main /main
USER nonroot:nonroot
ENTRYPOINT ["/main"]
