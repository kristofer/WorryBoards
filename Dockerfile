FROM golang:1.24-bookworm AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/worryboards .

FROM debian:bookworm-slim
WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/worryboards /app/worryboards
COPY problems.json /app/problems.json

ENV WORRYBOARDS_DB_PATH=/app/data/worryboards.db
ENV WORRYBOARDS_CATALOG_PATH=/app/problems.json

EXPOSE 8080
CMD ["/app/worryboards"]
