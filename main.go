package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "github.com/glebarez/go-sqlite"
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

type SolutionsData struct {
	ProblemID int
	Solutions []Solution
	NextCount int
}

type Catalog struct {
	Questions []CatalogQuestion `json:"questions"`
}

type CatalogQuestion struct {
	Difficulty int      `json:"difficulty"`
	Title      string   `json:"title"`
	Prompt     string   `json:"prompt"`
	Languages  []string `json:"languages"`
	Solutions  []string `json:"solutions"`
}

type ExpandedProblem struct {
	Language   string
	Difficulty int
	Title      string
	Prompt     string
	Solutions  []string
}

var supportedLanguages = map[string]struct{}{
	"Java":   {},
	"Python": {},
	"SQL":    {},
}

func main() {
	dbPath := envOrDefault("WORRYBOARDS_DB_PATH", "data/worryboards.db")
	catalogPath := envOrDefault("WORRYBOARDS_CATALOG_PATH", "problems.json")

	db, err := openAndInitDB(dbPath, catalogPath)
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

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func openAndInitDB(dbPath, catalogPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
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

	if err := migrateCatalog(db, catalogPath); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func initSchema(db *sql.DB) error {
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS app_meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`); err != nil {
		return err
	}

	if err := ensureCatalogTables(db); err != nil {
		return err
	}

	_, err := db.Exec(`
CREATE INDEX IF NOT EXISTS idx_problems_language_difficulty ON problems(language, difficulty);
CREATE INDEX IF NOT EXISTS idx_problem_solutions_problem_id ON problem_solutions(problem_id);
`)
	return err
}

func ensureCatalogTables(db *sql.DB) error {
	var existingProblemsSQL string
	err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'problems'").Scan(&existingProblemsSQL)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec(catalogTablesSchema)
		return err
	}

	if strings.Contains(existingProblemsSQL, "'SQL'") {
		_, err = db.Exec(catalogTablesSchema)
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DROP TABLE IF EXISTS problem_solutions"); err != nil {
		return err
	}
	if _, err := tx.Exec("DROP TABLE IF EXISTS problems"); err != nil {
		return err
	}
	if _, err := tx.Exec(catalogTablesSchema); err != nil {
		return err
	}

	return tx.Commit()
}

const catalogTablesSchema = `
CREATE TABLE IF NOT EXISTS problems (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	language TEXT NOT NULL CHECK(language IN ('Java', 'Python', 'SQL')),
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
`

func migrateCatalog(db *sql.DB, catalogPath string) error {
	catalog, raw, err := loadCatalog(catalogPath)
	if err != nil {
		return err
	}

	expanded, err := expandCatalog(catalog)
	if err != nil {
		return err
	}
	if len(expanded) == 0 {
		return errors.New("catalog has no problems")
	}

	checksum := hashBytes(raw)
	currentChecksum, err := getMetaValue(db, "catalog_checksum")
	if err != nil {
		return err
	}

	if currentChecksum == checksum {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM problems").Scan(&count); err != nil {
			return err
		}
		if count == len(expanded) {
			return nil
		}
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

	for _, problem := range expanded {
		res, err := problemInsert.Exec(problem.Language, problem.Difficulty, problem.Title, problem.Prompt)
		if err != nil {
			return err
		}
		problemID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for idx, solution := range problem.Solutions {
			if _, err := solutionInsert.Exec(problemID, idx+1, solution); err != nil {
				return err
			}
		}
	}

	if _, err := tx.Exec(
		"INSERT INTO app_meta(key, value) VALUES('catalog_checksum', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		checksum,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func loadCatalog(catalogPath string) (Catalog, []byte, error) {
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		return Catalog{}, nil, err
	}

	var catalog Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return Catalog{}, nil, err
	}
	if len(catalog.Questions) == 0 {
		return Catalog{}, nil, errors.New("catalog questions list is empty")
	}

	for i, question := range catalog.Questions {
		if question.Difficulty < 1 || question.Difficulty > 5 {
			return Catalog{}, nil, fmt.Errorf("question %d has invalid difficulty", i)
		}
		if strings.TrimSpace(question.Title) == "" {
			return Catalog{}, nil, fmt.Errorf("question %d has empty title", i)
		}
		if strings.TrimSpace(question.Prompt) == "" {
			return Catalog{}, nil, fmt.Errorf("question %d has empty prompt", i)
		}
		if len(question.Solutions) < 1 || len(question.Solutions) > 3 {
			return Catalog{}, nil, fmt.Errorf("question %d must have 1 to 3 solutions", i)
		}
		if len(question.Languages) == 0 {
			return Catalog{}, nil, fmt.Errorf("question %d has no languages", i)
		}
		seenLang := map[string]struct{}{}
		for _, language := range question.Languages {
			if _, ok := supportedLanguages[language]; !ok {
				return Catalog{}, nil, fmt.Errorf("question %d has unsupported language %q", i, language)
			}
			if _, exists := seenLang[language]; exists {
				return Catalog{}, nil, fmt.Errorf("question %d has duplicate language %q", i, language)
			}
			seenLang[language] = struct{}{}
		}
		for sIdx, solution := range question.Solutions {
			if strings.TrimSpace(solution) == "" {
				return Catalog{}, nil, fmt.Errorf("question %d solution %d is empty", i, sIdx)
			}
		}
	}

	return catalog, raw, nil
}

func expandCatalog(catalog Catalog) ([]ExpandedProblem, error) {
	perLanguageDifficultyIndex := map[string]int{}
	var expanded []ExpandedProblem

	for _, question := range catalog.Questions {
		sortedLanguages := append([]string{}, question.Languages...)
		sort.Strings(sortedLanguages)

		for _, language := range sortedLanguages {
			key := fmt.Sprintf("%s-%d", language, question.Difficulty)
			perLanguageDifficultyIndex[key]++
			index := perLanguageDifficultyIndex[key]

			title := fmt.Sprintf("%s D%d #%02d", question.Title, question.Difficulty, index)
			prompt := applyLanguageTemplate(question.Prompt, language)
			solutions := make([]string, len(question.Solutions))
			for i, solution := range question.Solutions {
				solutions[i] = applyLanguageTemplate(solution, language)
			}

			expanded = append(expanded, ExpandedProblem{
				Language:   language,
				Difficulty: question.Difficulty,
				Title:      title,
				Prompt:     prompt,
				Solutions:  solutions,
			})
		}
	}

	return expanded, nil
}

func applyLanguageTemplate(text, language string) string {
	text = strings.ReplaceAll(text, "{{language}}", language)
	return strings.ReplaceAll(text, "{language}", language)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func getMetaValue(db *sql.DB, key string) (string, error) {
	var value string
	err := db.QueryRow("SELECT value FROM app_meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
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

	count := 1
	if countParam := strings.TrimSpace(r.URL.Query().Get("count")); countParam != "" {
		parsedCount, err := strconv.Atoi(countParam)
		if err != nil || parsedCount < 1 {
			http.Error(w, "invalid count", http.StatusBadRequest)
			return
		}
		count = parsedCount
	}
	if count > len(solutions) {
		count = len(solutions)
	}

	data := SolutionsData{
		ProblemID: id,
		Solutions: solutions[:count],
	}
	if count < len(solutions) {
		data.NextCount = count + 1
	}

	if err := a.templates.ExecuteTemplate(w, "solutions", data); err != nil {
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
              <option value="SQL" {{if eq .Language "SQL"}}selected{{end}}>SQL</option>
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
  <script>
    function copyProblemPrompt(button, promptText) {
      if (!navigator.clipboard || !navigator.clipboard.writeText) {
        return;
      }
      navigator.clipboard.writeText(promptText).then(function () {
        const previous = button.textContent;
        button.textContent = "Copied!";
        setTimeout(function () {
          button.textContent = previous;
        }, 1200);
      });
    }
  </script>
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
<p class="mb-2">{{.Problem.Prompt}}</p>
<button class="btn btn-outline-primary btn-sm mb-3"
        type="button"
        onclick='copyProblemPrompt(this, "{{.Problem.Prompt | js}}")'>Copy question</button>
<div id="solutions-{{.Problem.ID}}"
     hx-swap="innerHTML">
  <button class="btn btn-outline-success btn-sm"
          hx-get="/problem/{{.Problem.ID}}/solutions?count=1"
          hx-target="#solutions-{{.Problem.ID}}"
          hx-swap="innerHTML"
          type="button">Show first hint</button>
</div>
{{end}}

{{define "solutions"}}
<h6>Possible Solutions</h6>
<ol class="mb-0">
  {{range .Solutions}}
    <li>{{.Solution}}</li>
  {{end}}
</ol>
{{if gt .NextCount 0}}
  <button class="btn btn-outline-success btn-sm mt-3"
          hx-get="/problem/{{.ProblemID}}/solutions?count={{.NextCount}}"
          hx-target="#solutions-{{.ProblemID}}"
          hx-swap="innerHTML"
          type="button">{{if eq .NextCount 2}}Show second hint{{else}}Show next hint{{end}}</button>
{{end}}
{{end}}
`
