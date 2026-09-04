package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := initSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if err := migrateCatalog(db, catalogPathForTest(t)); err != nil {
		t.Fatalf("migrate catalog: %v", err)
	}
	return db
}

func catalogPathForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "problems.json")
}

func TestCatalogPromptsIncludeExamplesAndNoLegacyBeginnerGuidance(t *testing.T) {
	b, err := os.ReadFile(catalogPathForTest(t))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}

	var catalog Catalog
	if err := json.Unmarshal(b, &catalog); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}

	for _, q := range catalog.Questions {
		if strings.Contains(q.Prompt, "Fundamentals task in {language}:") {
			t.Fatalf("legacy prompt format still present for %q", q.Title)
		}
		if !strings.Contains(strings.ToLower(q.Prompt), "example") {
			t.Fatalf("prompt %q should contain an example", q.Title)
		}
		if strings.Contains(strings.ToLower(q.Prompt), "keep the solution beginner") {
			t.Fatalf("prompt %q should not include legacy beginner-friendly guidance", q.Title)
		}
	}
}

func TestSeedProblemCounts(t *testing.T) {
	db := setupTestDB(t)

	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM problems").Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if total != 225 {
		t.Fatalf("expected 225 problems, got %d", total)
	}

	expectedByDifficulty := map[int]int{
		1: 97,
		2: 52,
		3: 31,
		4: 25,
		5: 20,
	}
	for difficulty := 1; difficulty <= 5; difficulty++ {
		var c int
		if err := db.QueryRow("SELECT COUNT(*) FROM problems WHERE difficulty = ?", difficulty).Scan(&c); err != nil {
			t.Fatalf("count difficulty %d: %v", difficulty, err)
		}
		expected := expectedByDifficulty[difficulty]
		if c != expected {
			t.Fatalf("expected %d problems for difficulty %d, got %d", expected, difficulty, c)
		}
	}

	expectedByLanguage := map[string]int{
		"Java":   124,
		"Python": 73,
		"SQL":    28,
	}
	for language, expected := range expectedByLanguage {
		var c int
		if err := db.QueryRow("SELECT COUNT(*) FROM problems WHERE language = ?", language).Scan(&c); err != nil {
			t.Fatalf("count language %s: %v", language, err)
		}
		if c != expected {
			t.Fatalf("expected %d problems for %s, got %d", expected, language, c)
		}
	}
}

func TestEachProblemHasOneToThreeSolutions(t *testing.T) {
	db := setupTestDB(t)

	rows, err := db.Query(`
SELECT problem_id, COUNT(*) as solution_count
FROM problem_solutions
GROUP BY problem_id`)
	if err != nil {
		t.Fatalf("query solution counts: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var problemID, count int
		if err := rows.Scan(&problemID, &count); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if count < 1 || count > 3 {
			t.Fatalf("problem %d has invalid solution count %d", problemID, count)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if seen != 225 {
		t.Fatalf("expected 225 seeded solution groups, got %d", seen)
	}
}

func TestEachProblemHasExpectedPotentialSolutions(t *testing.T) {
	db := setupTestDB(t)

	rows, err := db.Query(`
SELECT p.id, p.language, COUNT(pps.id) as potential_count
FROM problems p
LEFT JOIN problem_potential_solutions pps ON p.id = pps.problem_id
GROUP BY p.id, p.language`)
	if err != nil {
		t.Fatalf("query potential solution counts: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var problemID, count int
		var language string
		if err := rows.Scan(&problemID, &language, &count); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		expected := 2
		if language == "SQL" {
			expected = 1
		}
		if count != expected {
			t.Fatalf("problem %d (%s) expected %d potential solutions, got %d", problemID, language, expected, count)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if seen != 225 {
		t.Fatalf("expected 225 seeded potential-solution groups, got %d", seen)
	}
}

func TestCatalogPotentialSolutionsContainCodeSnippets(t *testing.T) {
	b, err := os.ReadFile(catalogPathForTest(t))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}

	var catalog Catalog
	if err := json.Unmarshal(b, &catalog); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}

	for _, q := range catalog.Questions {
		if len(q.Languages) == 1 && q.Languages[0] == "SQL" {
			sqlSnippet := q.PotentialSolutions["SQL"]
			hasSQLKeyword := strings.Contains(sqlSnippet, "SELECT") ||
				strings.Contains(sqlSnippet, "CREATE TABLE") ||
				strings.Contains(sqlSnippet, "INSERT") ||
				strings.Contains(sqlSnippet, "UPDATE") ||
				strings.Contains(sqlSnippet, "DELETE")
			if strings.TrimSpace(sqlSnippet) == "" || !hasSQLKeyword {
				t.Fatalf("expected SQL code snippet for %q, got: %s", q.Title, sqlSnippet)
			}
			continue
		}

		javaSnippet := q.PotentialSolutions["Java"]
		pythonSnippet := q.PotentialSolutions["Python"]
		if strings.TrimSpace(javaSnippet) == "" || !strings.Contains(javaSnippet, "class") {
			t.Fatalf("expected Java code snippet for %q, got: %s", q.Title, javaSnippet)
		}
		if strings.TrimSpace(pythonSnippet) == "" || !(strings.Contains(pythonSnippet, "def ") || strings.Contains(pythonSnippet, "for ") || strings.Contains(pythonSnippet, "class ") || strings.Contains(pythonSnippet, "print(") || strings.Contains(pythonSnippet, "=")) {
			t.Fatalf("expected Python code snippet for %q, got: %s", q.Title, pythonSnippet)
		}
	}
}

func TestGetProblemsRespectsFiltersAndLimit(t *testing.T) {
	db := setupTestDB(t)

	problems, err := getProblems(db, "Python", 4, 5)
	if err != nil {
		t.Fatalf("get problems: %v", err)
	}
	if len(problems) != 5 {
		t.Fatalf("expected 5 problems, got %d", len(problems))
	}
	for _, p := range problems {
		if p.Language != "Python" {
			t.Fatalf("unexpected language: %s", p.Language)
		}
		if p.Difficulty != 4 {
			t.Fatalf("unexpected difficulty: %d", p.Difficulty)
		}
	}
}

func TestGetProblemsSupportsSQL(t *testing.T) {
	db := setupTestDB(t)

	problems, err := getProblems(db, "SQL", 1, 5)
	if err != nil {
		t.Fatalf("get SQL problems: %v", err)
	}
	if len(problems) != 5 {
		t.Fatalf("expected 5 SQL problems, got %d", len(problems))
	}
	for _, p := range problems {
		if p.Language != "SQL" {
			t.Fatalf("unexpected language: %s", p.Language)
		}
		if p.Difficulty != 1 {
			t.Fatalf("unexpected difficulty: %d", p.Difficulty)
		}
	}
}

func TestLevelOneCoreTopicsExistForBothLanguages(t *testing.T) {
	db := setupTestDB(t)

	coreTopics := []string{
		"Print numbers 1 to 10",
		"Print even numbers from 2 to 20",
		"Sum numbers 1 to 100",
		"Countdown timer",
		"Multiplication table (single number)",
		"Basic string analysis",
		"Reverse a word (without slicing shortcuts)",
		"Count vowels in a sentence",
		"Write a function: letter frequency dictionary",
		"Find the largest number in a list",
		"Filter positive numbers",
		"Square numbers",
		"Basic dictionary practice",
		"Simple while input loop",
		"FizzBuzz (classic fundamentals)",
	}

	for _, language := range []string{"Java", "Python"} {
		for _, topic := range coreTopics {
			var c int
			if err := db.QueryRow(
				"SELECT COUNT(*) FROM problems WHERE difficulty = 1 AND language = ? AND title GLOB ?",
				language,
				topic+" D1 #[0-9][0-9]",
			).Scan(&c); err != nil {
				t.Fatalf("count topic %q for %s: %v", topic, language, err)
			}
			if c != 1 {
				t.Fatalf("expected exactly one difficulty-1 problem for %q in %s, got %d", topic, language, c)
			}
		}
	}
}

func TestPythonStarterOnlyTopicsExist(t *testing.T) {
	db := setupTestDB(t)

	topics := []string{
		"Drop the first item from a list",
		"Drop the last two items from a list",
		"Concatenate two lists",
	}

	for _, topic := range topics {
		var pythonCount int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM problems WHERE difficulty = 1 AND language = 'Python' AND title GLOB ?",
			topic+" D1 #[0-9][0-9]",
		).Scan(&pythonCount); err != nil {
			t.Fatalf("count python topic %q: %v", topic, err)
		}
		if pythonCount != 1 {
			t.Fatalf("expected exactly one Python difficulty-1 problem for %q, got %d", topic, pythonCount)
		}

		var javaCount int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM problems WHERE difficulty = 1 AND language = 'Java' AND title GLOB ?",
			topic+" D1 #[0-9][0-9]",
		).Scan(&javaCount); err != nil {
			t.Fatalf("count java topic %q: %v", topic, err)
		}
		if javaCount != 0 {
			t.Fatalf("expected no Java difficulty-1 problem for %q, got %d", topic, javaCount)
		}
	}
}

func TestSQLStarterTopicsExist(t *testing.T) {
	db := setupTestDB(t)

	topics := []string{
		"What is the SELECT statement?",
		"Common clauses used with SELECT",
		"Entities and relationships in databases",
		"Common database relationship types",
		"What is a primary key?",
		"What is a foreign key?",
		"What is a query?",
		"What does the WHERE clause do?",
		"Sort rows with ORDER BY",
		"Limit result size with LIMIT",
		"Remove duplicates with DISTINCT",
		"Rename columns with aliases",
		"Filter with IN",
		"Filter with BETWEEN",
		"Search text with LIKE",
		"Understand NULL and IS NULL",
		"Count rows with COUNT",
		"Find max and min values",
		"Compute averages with AVG",
		"Sum a numeric column",
		"Group rows with GROUP BY",
		"Filter grouped results with HAVING",
		"Join two tables with INNER JOIN",
		"Keep unmatched rows with LEFT JOIN",
		"Add new rows with INSERT",
		"Modify rows with UPDATE",
		"Remove rows with DELETE",
	}

	for _, topic := range topics {
		var sqlCount int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM problems WHERE language = 'SQL' AND difficulty = 1 AND title GLOB ?",
			topic+" D1 #[0-9][0-9]",
		).Scan(&sqlCount); err != nil {
			t.Fatalf("count SQL topic %q: %v", topic, err)
		}
		if sqlCount != 1 {
			t.Fatalf("expected exactly one SQL difficulty-1 problem for %q, got %d", topic, sqlCount)
		}
	}

	var advancedCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM problems WHERE language = 'SQL' AND difficulty = 2 AND title GLOB 'What is a subquery and what are its types? D2 #[0-9][0-9]'",
	).Scan(&advancedCount); err != nil {
		t.Fatalf("count SQL subquery topic: %v", err)
	}
	if advancedCount != 1 {
		t.Fatalf("expected exactly one SQL difficulty-2 subquery problem, got %d", advancedCount)
	}
}

func TestLevelTwoSharedTopicsIncludeNewJavaAndPythonQuestions(t *testing.T) {
	db := setupTestDB(t)

	topics := []string{
		"Validate Binary Search Tree",
		"Coin Change Minimum Coins",
		"Number of Islands",
		"Min Stack Operations",
		"Word Search Backtracking",
	}

	for _, language := range []string{"Java", "Python"} {
		for _, topic := range topics {
			var c int
			if err := db.QueryRow(
				"SELECT COUNT(*) FROM problems WHERE difficulty = 2 AND language = ? AND title GLOB ?",
				language,
				topic+" D2 #[0-9][0-9]",
			).Scan(&c); err != nil {
				t.Fatalf("count topic %q for %s: %v", topic, language, err)
			}
			if c != 1 {
				t.Fatalf("expected exactly one difficulty-2 problem for %q in %s, got %d", topic, language, c)
			}
		}
	}
}

func TestInitSchemaUpgradesProblemLanguageConstraint(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	oldSchema := `
	CREATE TABLE problems (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		language TEXT NOT NULL CHECK(language IN ('Java', 'Python')),
		difficulty INTEGER NOT NULL CHECK(difficulty BETWEEN 1 AND 5),
		title TEXT NOT NULL,
		prompt TEXT NOT NULL
	);
	CREATE TABLE problem_solutions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		problem_id INTEGER NOT NULL,
		solution_order INTEGER NOT NULL,
		solution TEXT NOT NULL,
		FOREIGN KEY(problem_id) REFERENCES problems(id) ON DELETE CASCADE
	);
	`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}

	if err := initSchema(db); err != nil {
		t.Fatalf("upgrade schema: %v", err)
	}
	if err := migrateCatalog(db, catalogPathForTest(t)); err != nil {
		t.Fatalf("migrate upgraded schema catalog: %v", err)
	}

	var c int
	if err := db.QueryRow("SELECT COUNT(*) FROM problems WHERE language = 'SQL'").Scan(&c); err != nil {
		t.Fatalf("count SQL after schema upgrade: %v", err)
	}
	if c == 0 {
		t.Fatal("expected SQL problems after upgrading old schema")
	}

	var potentialCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM problem_potential_solutions").Scan(&potentialCount); err != nil {
		t.Fatalf("count potential solutions after schema upgrade: %v", err)
	}
	if potentialCount == 0 {
		t.Fatal("expected potential solutions after upgrading old schema")
	}
}

func TestMigrateCatalogUpdatesWhenJSONChanges(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	tmpDir := t.TempDir()
	catalogPath := filepath.Join(tmpDir, "catalog.json")

	firstCatalog := `{"questions":[{"difficulty":1,"title":"A","prompt":"Do A in {{language}}","languages":["Java"],"solutions":["S1"],"potential_solutions":{"Java":"Java sol A","Python":"Python sol A"}}]}`
	if err := os.WriteFile(catalogPath, []byte(firstCatalog), 0o644); err != nil {
		t.Fatalf("write first catalog: %v", err)
	}
	if err := migrateCatalog(db, catalogPath); err != nil {
		t.Fatalf("migrate first catalog: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM problems").Scan(&count); err != nil {
		t.Fatalf("count after first migrate: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 problem after first migration, got %d", count)
	}

	secondCatalog := `{"questions":[{"difficulty":1,"title":"A","prompt":"Do A in {{language}}","languages":["Java"],"solutions":["S1"],"potential_solutions":{"Java":"Java sol A","Python":"Python sol A"}},{"difficulty":1,"title":"B","prompt":"Do B in {{language}}","languages":["Java"],"solutions":["S1","S2"],"potential_solutions":{"Java":"Java sol B","Python":"Python sol B"}}]}`
	if err := os.WriteFile(catalogPath, []byte(secondCatalog), 0o644); err != nil {
		t.Fatalf("write second catalog: %v", err)
	}
	if err := migrateCatalog(db, catalogPath); err != nil {
		t.Fatalf("migrate second catalog: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM problems").Scan(&count); err != nil {
		t.Fatalf("count after second migrate: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 problems after second migration, got %d", count)
	}
}

func TestSelectedProblemUsesClickToRevealFirstHint(t *testing.T) {
	app := newTestApp(t)
	problemID := firstProblemID(t, app.db)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/problem/%d", problemID), nil)
	rr := httptest.NewRecorder()
	app.handleProblemSelection(rr, req, problemID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "Show first hint") {
		t.Fatalf("expected first-hint button in selected problem response, got: %s", body)
	}
	if !strings.Contains(body, "Copy question") || !strings.Contains(body, "copyProblemPrompt(this") {
		t.Fatalf("expected copy-question button in selected problem response, got: %s", body)
	}
	if !strings.Contains(body, `id="actual-solution-button-`) ||
		!strings.Contains(body, "Show actual solution (available in 60s)") ||
		!strings.Contains(body, `data-problem-id="`) ||
		!strings.Contains(body, `data-unlock-seconds="60"`) ||
		!strings.Contains(body, `/actual-solution", "actual-solution-`) ||
		!strings.Contains(body, "revealActualSolutionAfterDelay(this") {
		t.Fatalf("expected disabled actual-solution button with auto-unlock in selected problem response, got: %s", body)
	}
	if !strings.Contains(body, `aria-describedby="actual-solution-status-`) ||
		!strings.Contains(body, `id="actual-solution-status-`) ||
		!strings.Contains(body, "Actual solution available in 60 seconds.") {
		t.Fatalf("expected accessible status text for actual-solution button unlock timing, got: %s", body)
	}
	if strings.Index(body, "Show first hint") > strings.Index(body, "Show actual solution (available in 60s)") {
		t.Fatalf("expected actual-solution button to render below hint button area, got: %s", body)
	}
}

func TestProblemSolutionsRevealHintsIncrementally(t *testing.T) {
	app := newTestApp(t)
	problemID := problemIDWithSolutionCount(t, app.db, 2)

	firstReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/problem/%d/solutions?count=1", problemID), nil)
	firstRes := httptest.NewRecorder()
	app.handleProblemSolutions(firstRes, firstReq, problemID)

	if firstRes.Code != http.StatusOK {
		t.Fatalf("expected status 200 for first hint, got %d", firstRes.Code)
	}
	firstBody := firstRes.Body.String()
	if strings.Count(firstBody, "<li>") != 1 {
		t.Fatalf("expected 1 revealed hint, got response: %s", firstBody)
	}
	if !strings.Contains(firstBody, "Show second hint") {
		t.Fatalf("expected follow-up button for second hint, got: %s", firstBody)
	}

	secondReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/problem/%d/solutions?count=2", problemID), nil)
	secondRes := httptest.NewRecorder()
	app.handleProblemSolutions(secondRes, secondReq, problemID)

	if secondRes.Code != http.StatusOK {
		t.Fatalf("expected status 200 for second hint, got %d", secondRes.Code)
	}
	secondBody := secondRes.Body.String()
	if strings.Count(secondBody, "<li>") != 2 {
		t.Fatalf("expected 2 revealed hints, got response: %s", secondBody)
	}
	if strings.Contains(secondBody, "Show second hint") || strings.Contains(secondBody, "Show next hint") {
		t.Fatalf("did not expect another hint button after revealing second hint, got: %s", secondBody)
	}
}

func TestProblemPotentialSolutionsShowsSelectedLanguageOnly(t *testing.T) {
	app := newTestApp(t)
	problemID := firstProblemIDForLanguage(t, app.db, "Java")

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/problem/%d/actual-solution", problemID), nil)
	rr := httptest.NewRecorder()
	app.handleProblemActualSolutions(rr, req, problemID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, ">Java<") {
		t.Fatalf("expected Java labeled potential solution, got: %s", body)
	}
	if strings.Contains(body, ">Python<") || strings.Contains(body, ">SQL<") {
		t.Fatalf("did not expect non-selected-language labels, got: %s", body)
	}
	if !strings.Contains(strings.ToLower(body), "actual solution") {
		t.Fatalf("expected actual solution heading, got: %s", body)
	}
	if !strings.Contains(body, "<pre") || !strings.Contains(body, "<code>") {
		t.Fatalf("expected code formatting in rendered actual solutions, got: %s", body)
	}
	if !strings.Contains(body, "public class Solution") {
		t.Fatalf("expected Java code snippet in response, got: %s", body)
	}
	if strings.Contains(body, "range(1, 11)") {
		t.Fatalf("did not expect Python code snippet in response, got: %s", body)
	}
}

func TestProblemPotentialSolutionsShowsSQLForSQLProblem(t *testing.T) {
	app := newTestApp(t)
	problemID := firstProblemIDForLanguage(t, app.db, "SQL")

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/problem/%d/actual-solution", problemID), nil)
	rr := httptest.NewRecorder()
	app.handleProblemActualSolutions(rr, req, problemID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, ">SQL<") {
		t.Fatalf("expected SQL labeled potential solution, got: %s", body)
	}
	if strings.Contains(body, ">Java<") || strings.Contains(body, ">Python<") {
		t.Fatalf("did not expect Java/Python labels for SQL problem, got: %s", body)
	}
	if !strings.Contains(body, "<pre") || !strings.Contains(body, "<code>") || !strings.Contains(body, "SELECT") {
		t.Fatalf("expected SQL code snippet formatting and content, got: %s", body)
	}
}

func TestHomePageFiltersAutoLoadOnMenuChange(t *testing.T) {
	app := newTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	app.handleHome(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `hx-trigger="change from:select, submit"`) {
		t.Fatalf("expected auto-refresh trigger on filter form, got: %s", body)
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()

	return &App{
		db:        setupTestDB(t),
		templates: template.Must(template.New("all").Parse(templates)),
	}
}

func firstProblemID(t *testing.T, db *sql.DB) int {
	t.Helper()

	var id int
	if err := db.QueryRow("SELECT id FROM problems ORDER BY id LIMIT 1").Scan(&id); err != nil {
		t.Fatalf("select first problem id: %v", err)
	}
	return id
}

func problemIDWithSolutionCount(t *testing.T, db *sql.DB, expectedCount int) int {
	t.Helper()

	var id int
	err := db.QueryRow(`
SELECT problem_id
FROM problem_solutions
GROUP BY problem_id
HAVING COUNT(*) = ?
ORDER BY problem_id
LIMIT 1`, expectedCount).Scan(&id)
	if err != nil {
		t.Fatalf("select problem id with %d solutions: %v", expectedCount, err)
	}
	return id
}

func firstProblemIDForLanguage(t *testing.T, db *sql.DB, language string) int {
	t.Helper()

	var id int
	if err := db.QueryRow("SELECT id FROM problems WHERE language = ? ORDER BY id LIMIT 1", language).Scan(&id); err != nil {
		t.Fatalf("select first %s problem id: %v", language, err)
	}
	return id
}
