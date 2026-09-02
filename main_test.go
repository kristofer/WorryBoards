package main

import (
	"database/sql"
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
	if err := seedProblems(db); err != nil {
		t.Fatalf("seed problems: %v", err)
	}
	return db
}

func TestSeedProblemCounts(t *testing.T) {
	db := setupTestDB(t)

	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM problems").Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if total != 100 {
		t.Fatalf("expected 100 problems, got %d", total)
	}

	for difficulty := 1; difficulty <= 5; difficulty++ {
		var c int
		if err := db.QueryRow("SELECT COUNT(*) FROM problems WHERE difficulty = ?", difficulty).Scan(&c); err != nil {
			t.Fatalf("count difficulty %d: %v", difficulty, err)
		}
		if c != 20 {
			t.Fatalf("expected 20 problems for difficulty %d, got %d", difficulty, c)
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
	if seen != 100 {
		t.Fatalf("expected 100 seeded solution groups, got %d", seen)
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
