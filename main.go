package main

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

type Problem struct {
	ID         int
	Language   string
	Difficulty int
	Title      string
	Prompt     string
}

type Solution struct {
	SolutionOrder int
	Solution      string
}

type App struct {
	db        *sql.DB
	templates *template.Template
}

type PageData struct {
	Language   string
	Difficulty int
	Problems   []Problem
}

type ProblemListData struct {
	Language   string
	Difficulty int
	Problems   []Problem
}

type SelectedProblemData struct {
	Problem Problem
}

var supportedLanguages = map[string]struct{}{
	"Java":   {},
	"Python": {},
}

func main() {
	db, err := openAndInitDB("/home/runner/work/WorryBoards/WorryBoards/data/worryboards.db")
	if err != nil {
		log.Fatalf("failed to initialize database: %v", err)
	}
	defer db.Close()

	app := &App{
		db:        db,
		templates: template.Must(template.New("all").Parse(templates)),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleHome)
	mux.HandleFunc("/problems", app.handleProblems)
	mux.HandleFunc("/problem/", app.handleProblemRoutes)

	log.Println("WorryBoards running at http://localhost:8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatal(err)
	}
}

func openAndInitDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA temp_store = MEMORY;",
		"PRAGMA foreign_keys = ON;",
	}
	for _, stmt := range pragmas {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}

	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := seedProblems(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func initSchema(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS problems (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	language TEXT NOT NULL CHECK(language IN ('Java', 'Python')),
	difficulty INTEGER NOT NULL CHECK(difficulty BETWEEN 1 AND 5),
	title TEXT NOT NULL,
	prompt TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS problem_solutions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	problem_id INTEGER NOT NULL,
	solution_order INTEGER NOT NULL,
	solution TEXT NOT NULL,
	FOREIGN KEY(problem_id) REFERENCES problems(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_problems_language_difficulty ON problems(language, difficulty);
CREATE INDEX IF NOT EXISTS idx_problem_solutions_problem_id ON problem_solutions(problem_id);
`
	_, err := db.Exec(schema)
	return err
}

func seedProblems(db *sql.DB) error {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM problems").Scan(&count); err != nil {
		return err
	}

	if count == 100 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM problem_solutions"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM problems"); err != nil {
		return err
	}

	topics := []string{
		"Two Sum Variant",
		"Balanced Brackets",
		"Reverse Linked List",
		"Merge Intervals",
		"Top K Frequent",
		"Binary Tree Depth",
		"LRU Cache Design",
		"Matrix Rotation",
		"Anagram Grouping",
		"Sliding Window Maximum",
	}

	problemInsert, err := tx.Prepare("INSERT INTO problems(language, difficulty, title, prompt) VALUES(?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer problemInsert.Close()

	solutionInsert, err := tx.Prepare("INSERT INTO problem_solutions(problem_id, solution_order, solution) VALUES(?, ?, ?)")
	if err != nil {
		return err
	}
	defer solutionInsert.Close()

	for difficulty := 1; difficulty <= 5; difficulty++ {
		for _, language := range []string{"Java", "Python"} {
			for i, topic := range topics {
				title := fmt.Sprintf("%s D%d #%02d", topic, difficulty, i+1)
				prompt := fmt.Sprintf(
					"Implement %s in %s. Target difficulty %d. Discuss runtime and edge cases.",
					topic, language, difficulty,
				)
				res, err := problemInsert.Exec(language, difficulty, title, prompt)
				if err != nil {
					return err
				}
				problemID, err := res.LastInsertId()
				if err != nil {
					return err
				}

				solutionCount := 1 + ((difficulty + i) % 3)
				for s := 1; s <= solutionCount; s++ {
					solution := buildSolutionText(language, topic, s)
					if _, err := solutionInsert.Exec(problemID, s, solution); err != nil {
						return err
					}
				}
			}
		}
	}

	return tx.Commit()
}

func buildSolutionText(language, topic string, variant int) string {
	switch variant {
	case 1:
		return fmt.Sprintf("%s approach: direct implementation using core data structures for %s.", language, topic)
	case 2:
		return fmt.Sprintf("%s approach: optimize time complexity with hashing or memoization for %s.", language, topic)
	default:
		return fmt.Sprintf("%s approach: optimize space usage while keeping code interview-friendly for %s.", language, topic)
	}
}

func parseRequestFilters(r *http.Request) (string, int) {
	language := strings.TrimSpace(r.URL.Query().Get("language"))
	if _, ok := supportedLanguages[language]; !ok {
		language = "Java"
	}

	difficulty := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("difficulty")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= 5 {
			difficulty = parsed
		}
	}

	return language, difficulty
}

func getProblems(db *sql.DB, language string, difficulty, limit int) ([]Problem, error) {
	rows, err := db.Query(`
SELECT id, language, difficulty, title, prompt
FROM problems
WHERE language = ? AND difficulty = ?
ORDER BY RANDOM()
LIMIT ?`, language, difficulty, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Problem
	for rows.Next() {
		var p Problem
		if err := rows.Scan(&p.ID, &p.Language, &p.Difficulty, &p.Title, &p.Prompt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func getProblemByID(db *sql.DB, id int) (Problem, error) {
	var p Problem
	err := db.QueryRow(`
SELECT id, language, difficulty, title, prompt
FROM problems
WHERE id = ?`, id).Scan(&p.ID, &p.Language, &p.Difficulty, &p.Title, &p.Prompt)
	if err != nil {
		return Problem{}, err
	}
	return p, nil
}

func getProblemSolutions(db *sql.DB, problemID int) ([]Solution, error) {
	rows, err := db.Query(`
SELECT solution_order, solution
FROM problem_solutions
WHERE problem_id = ?
ORDER BY solution_order ASC`, problemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Solution
	for rows.Next() {
		var s Solution
		if err := rows.Scan(&s.SolutionOrder, &s.Solution); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	language, difficulty := parseRequestFilters(r)
	problems, err := getProblems(a.db, language, difficulty, 5)
	if err != nil {
		http.Error(w, "failed to load problems", http.StatusInternalServerError)
		return
	}

	data := PageData{
		Language:   language,
		Difficulty: difficulty,
		Problems:   problems,
	}

	if err := a.templates.ExecuteTemplate(w, "page", data); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
	}
}

func (a *App) handleProblems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	language, difficulty := parseRequestFilters(r)
	problems, err := getProblems(a.db, language, difficulty, 5)
	if err != nil {
		http.Error(w, "failed to load problems", http.StatusInternalServerError)
		return
	}

	data := ProblemListData{
		Language:   language,
		Difficulty: difficulty,
		Problems:   problems,
	}

	if err := a.templates.ExecuteTemplate(w, "problem_list", data); err != nil {
		http.Error(w, "failed to render list", http.StatusInternalServerError)
	}
}

func (a *App) handleProblemRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/problem/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(path, "/")
	id, err := strconv.Atoi(parts[0])
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 1 {
		a.handleProblemSelection(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "solutions" {
		a.handleProblemSolutions(w, r, id)
		return
	}
	http.NotFound(w, r)
}

func (a *App) handleProblemSelection(w http.ResponseWriter, r *http.Request, id int) {
	problem, err := getProblemByID(a.db, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to load problem", http.StatusInternalServerError)
		return
	}

	data := SelectedProblemData{Problem: problem}
	if err := a.templates.ExecuteTemplate(w, "selected_problem", data); err != nil {
		http.Error(w, "failed to render problem", http.StatusInternalServerError)
	}
}

func (a *App) handleProblemSolutions(w http.ResponseWriter, r *http.Request, id int) {
	solutions, err := getProblemSolutions(a.db, id)
	if err != nil {
		http.Error(w, "failed to load solutions", http.StatusInternalServerError)
		return
	}
	if len(solutions) == 0 {
		http.NotFound(w, r)
		return
	}
	if err := a.templates.ExecuteTemplate(w, "solutions", solutions); err != nil {
		http.Error(w, "failed to render solutions", http.StatusInternalServerError)
	}
}

const templates = `
{{define "page"}}
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>WorryBoards</title>
  <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css" rel="stylesheet">
  <script src="https://unpkg.com/htmx.org@1.9.12"></script>
</head>
<body class="bg-light">
  <div class="container py-4">
    <div class="row mb-4">
      <div class="col">
        <h1 class="display-6 text-primary">WorryBoards</h1>
        <p class="text-secondary mb-0">Pick a language, choose a difficulty, then select a whiteboard prompt.</p>
      </div>
    </div>

    <div class="card shadow-sm mb-4">
      <div class="card-body">
        <form id="filters" class="row g-3"
              hx-get="/problems"
              hx-target="#problem-list"
              hx-swap="innerHTML">
          <div class="col-md-4">
            <label class="form-label">Language</label>
            <select class="form-select" name="language">
              <option value="Java" {{if eq .Language "Java"}}selected{{end}}>Java</option>
              <option value="Python" {{if eq .Language "Python"}}selected{{end}}>Python</option>
            </select>
          </div>
          <div class="col-md-4">
            <label class="form-label">Difficulty (1-5)</label>
            <select class="form-select" name="difficulty">
              <option value="1" {{if eq .Difficulty 1}}selected{{end}}>1 - Easy peasy</option>
              <option value="2" {{if eq .Difficulty 2}}selected{{end}}>2</option>
              <option value="3" {{if eq .Difficulty 3}}selected{{end}}>3</option>
              <option value="4" {{if eq .Difficulty 4}}selected{{end}}>4</option>
              <option value="5" {{if eq .Difficulty 5}}selected{{end}}>5 - Intermediate hard</option>
            </select>
          </div>
          <div class="col-md-4 d-flex align-items-end">
            <button class="btn btn-primary w-100" type="submit">Show 5 Problems</button>
          </div>
        </form>
      </div>
    </div>

    <div class="row g-4">
      <div class="col-lg-6">
        <div class="card shadow-sm h-100">
          <div class="card-header bg-primary text-white">Available Problems</div>
          <div class="card-body" id="problem-list">{{template "problem_list" .}}</div>
        </div>
      </div>
      <div class="col-lg-6">
        <div class="card shadow-sm h-100">
          <div class="card-header bg-success text-white">Selected Problem</div>
          <div class="card-body" id="selected-problem">
            <div class="text-muted">Choose a problem from the left to reveal prompt and solutions.</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</body>
</html>
{{end}}

{{define "problem_list"}}
{{if not .Problems}}
  <div class="alert alert-warning mb-0">No problems found.</div>
{{else}}
  <div class="list-group">
  {{range .Problems}}
    <button class="list-group-item list-group-item-action"
            hx-get="/problem/{{.ID}}"
            hx-target="#selected-problem"
            hx-swap="innerHTML">
      <div class="d-flex w-100 justify-content-between">
        <h6 class="mb-1">{{.Title}}</h6>
        <small class="text-body-secondary">D{{.Difficulty}}</small>
      </div>
      <small class="text-body-secondary">{{.Language}}</small>
    </button>
  {{end}}
  </div>
{{end}}
{{end}}

{{define "selected_problem"}}
<h5 class="text-success">{{.Problem.Title}}</h5>
<p class="text-body-secondary mb-1">{{.Problem.Language}} · Difficulty {{.Problem.Difficulty}}</p>
<p class="mb-3">{{.Problem.Prompt}}</p>
<div id="solutions-{{.Problem.ID}}"
     hx-get="/problem/{{.Problem.ID}}/solutions"
     hx-trigger="load delay:3s"
     hx-swap="innerHTML">
  <div class="alert alert-info mb-0">Possible solutions unlock in a few seconds...</div>
</div>
{{end}}

{{define "solutions"}}
<h6>Possible Solutions</h6>
<ol class="mb-0">
  {{range .}}
    <li>{{.Solution}}</li>
  {{end}}
</ol>
{{end}}
`
