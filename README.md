# WorryBoards

A static whiteboard practice app built with Go, HTMX, Bootstrap, and SQLite.

Seeded catalog:
- 195 total problems
- Language choices: Java, Python, SQL
- Difficulty 1 has 77 fundamentals-focused prompts
- Difficulty 2 has 42 prompts
- Difficulty 3 has 31 prompts
- Difficulty 4 has 25 prompts
- Difficulty 5 has 20 prompts
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
