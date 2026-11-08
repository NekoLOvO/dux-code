package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// setup creates a fake HOME and skills directory.
func setup(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func put(t *testing.T, root, dir, name, content string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestScanReportsMissingSkillMd covers what a user actually hits: a test.md is dropped in,
// nothing appears, and there is no clue why.
func TestScanReportsMissingSkillMd(t *testing.T) {
	root := setup(t)
	put(t, root, "test", "test.md", "whatever")

	scan, err := ScanSkills()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Skills) != 0 {
		t.Errorf("must not count as a skill: %+v", scan.Skills)
	}
	if len(scan.Skipped) != 1 {
		t.Fatalf("one folder should be reported as skipped: %+v", scan.Skipped)
	}
	if scan.Skipped[0].Name != "test" {
		t.Errorf("wrong name: %s", scan.Skipped[0].Name)
	}
	if scan.Skipped[0].Reason == "" {
		t.Error("a reason is required, or the user can only guess")
	}
}

// TestScanAcceptsLowerCaseSkillMd checks that skill.md counts — insisting on uppercase is just
// hostile.
func TestScanAcceptsLowerCaseSkillMd(t *testing.T) {
	root := setup(t)
	put(t, root, "a", "skill.md", "---\nname: lowercase\ndescription: fine\n---\n\nbody")

	scan, err := ScanSkills()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Skills) != 1 || scan.Skills[0].Name != "lowercase" {
		t.Fatalf("a lowercase filename should be recognized: %+v", scan.Skills)
	}
}

// TestScanWarnsWhenDescriptionMissing checks that "SKILL.md but no description" is reported: it
// never reaches the system prompt list, so the model cannot see it.
func TestScanWarnsWhenDescriptionMissing(t *testing.T) {
	root := setup(t)
	put(t, root, "b", "SKILL.md", "---\nname: no description\n---\n\nbody")

	scan, err := ScanSkills()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Skills) != 1 {
		t.Fatalf("it should still be listed: %+v", scan.Skills)
	}
	// Not a separate skipped entry — one skill is one row.
	if len(scan.Skipped) != 0 {
		t.Fatalf("it must not also appear in skipped: %+v", scan.Skipped)
	}
	if scan.Skills[0].Warning == "" {
		t.Error("the skill should carry a warning that the model cannot see it")
	}
}

// TestScanCleanCase checks that the normal path reports nothing skipped.
func TestScanCleanCase(t *testing.T) {
	root := setup(t)
	put(t, root, "c", "SKILL.md", "---\nname: normal\ndescription: fine\n---\n\nbody")

	scan, err := ScanSkills()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Skills) != 1 {
		t.Fatalf("there should be one: %+v", scan.Skills)
	}
	if len(scan.Skipped) != 0 {
		t.Errorf("nothing should be skipped: %+v", scan.Skipped)
	}
}

// TestScanMissingDirIsNotAnError checks that nothing installed is an empty set rather than an
// error — the usual first-run state.
func TestScanMissingDirIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	scan, err := ScanSkills()
	if err != nil {
		t.Fatalf("this is not an error: %v", err)
	}
	if len(scan.Skills) != 0 || len(scan.Skipped) != 0 {
		t.Errorf("should be empty: %+v", scan)
	}
}
