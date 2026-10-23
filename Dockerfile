FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jwars ./cmd/jwars

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/jwars /app/jwars
COPY variables/ /app/variables/
ENV JWARS_ADDR=0.0.0.0:8080 \
    JWARS_SEED_DEV_PLAYER=false \
    JWARS_VARIABLES_DIR=/app/variables
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/jwars"]
