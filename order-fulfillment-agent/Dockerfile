# ---------- build stage ----------
FROM golang:1.23-bookworm AS build
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO needed for the sqlite driver; static build so distroless works.
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w -linkmode external -extldflags '-static'" \
    -o /out/agent ./cmd/agent

# ---------- runtime stage ----------
FROM gcr.io/distroless/base-debian12:latest
COPY --from=build /out/agent /agent
ENV PORT=8080 DB_DSN=/tmp/agent.db
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/agent"]
CMD ["serve"]
