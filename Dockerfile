FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/signal-agent ./cmd/signal-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/signal-agent /signal-agent
EXPOSE 4317 4318
USER nonroot:nonroot
ENTRYPOINT ["/signal-agent"]
