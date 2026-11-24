FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
ENV LISTEN_ADDR=:8080 \
    DB_PATH=/data/gateway.db
COPY --from=build /out/gateway /gateway
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/gateway"]
