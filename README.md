# WorryBoards

A static whiteboard practice app built with Go, HTMX, Bootstrap, and SQLite.

Seeded catalog:
- 130 total problems
- Difficulty 1 has 50 fundamentals-focused prompts
- Difficulties 2-5 have 20 prompts each
- Source of truth is `/home/runner/work/WorryBoards/WorryBoards/problems.json`
- On startup, the app runs a migration/sync that checks the JSON and updates SQLite when the catalog changes

## Run locally

```bash
go run .
```

Then open `http://localhost:8080`.

## Test

```bash
go test ./...
```

## Run with Docker Compose

```bash
docker compose up --build
```
