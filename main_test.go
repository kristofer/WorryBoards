package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
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

func TestSeedProblemCounts(t *testing.T) {
	db := setupTestDB(t)

	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM problems").Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if total != 133 {
		t.Fatalf("expected 133 problems, got %d", total)
	}

	for difficulty := 1; difficulty <= 5; difficulty++ {
		var c int
		if err := db.QueryRow("SELECT COUNT(*) FROM problems WHERE difficulty = ?", difficulty).Scan(&c); err != nil {
			t.Fatalf("count difficulty %d: %v", difficulty, err)
		}
		expected := 20
		if difficulty == 1 {
			expected = 53
		}
		if c != expected {
			t.Fatalf("expected %d problems for difficulty %d, got %d", expected, difficulty, c)
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
	if seen != 133 {
		t.Fatalf("expected 133 seeded solution groups, got %d", seen)
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
		"Filter positive numbers (list comprehension)",
		"Square numbers (list comprehension)",
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

func TestMigrateCatalogUpdatesWhenJSONChanges(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	tmpDir := t.TempDir()
	catalogPath := filepath.Join(tmpDir, "catalog.json")

	firstCatalog := `{"questions":[{"difficulty":1,"title":"A","prompt":"Do A in {{language}}","languages":["Java"],"solutions":["S1"]}]}`
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

	secondCatalog := `{"questions":[{"difficulty":1,"title":"A","prompt":"Do A in {{language}}","languages":["Java"],"solutions":["S1"]},{"difficulty":1,"title":"B","prompt":"Do B in {{language}}","languages":["Java"],"solutions":["S1","S2"]}]}`
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
