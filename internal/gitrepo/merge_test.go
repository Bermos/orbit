package gitrepo

import "testing"

func TestParseDiff(t *testing.T) {
	out := `diff --git a/calc.go b/calc.go
index 1111111..2222222 100644
--- a/calc.go
+++ b/calc.go
@@ -2,3 +2,4 @@ package calc
 
-func Add(a, b int) int { return a - b }
+func Add(a, b int) int { return a + b }
+
 // end
\ No newline at end of file
diff --git a/old name.txt b/new name.txt
similarity index 90%
rename from old name.txt
rename to new name.txt
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..3333333
Binary files /dev/null and b/logo.png differ
diff --git "a/caf\303\251.txt" "b/caf\303\251.txt"
deleted file mode 100644
index 4444444..0000000
--- "a/caf\303\251.txt"
+++ /dev/null
@@ -1 +0,0 @@
-gone
`
	files := ParseDiff([]byte(out))
	if len(files) != 4 {
		t.Fatalf("files = %+v", files)
	}
	f := files[0]
	if f.NewPath != "calc.go" || f.Status != "modified" || f.Additions != 2 || f.Deletions != 1 || len(f.Hunks) != 1 {
		t.Fatalf("calc.go = %+v", f)
	}
	lines := f.Hunks[0].Lines
	if len(lines) != 5 || lines[0].Kind != "context" || lines[0].Old != 2 || lines[0].New != 2 ||
		lines[1].Kind != "del" || lines[1].Old != 3 || lines[2].Kind != "add" || lines[2].New != 3 || lines[4].Old != 4 || lines[4].New != 5 {
		t.Fatalf("lines = %+v", lines)
	}
	if r := files[1]; r.Status != "renamed" || r.OldPath != "old name.txt" || r.NewPath != "new name.txt" {
		t.Fatalf("rename = %+v", r)
	}
	if b := files[2]; b.Status != "added" || !b.Binary || b.NewPath != "logo.png" {
		t.Fatalf("binary = %+v", b)
	}
	if d := files[3]; d.Status != "deleted" || d.OldPath != "café.txt" || d.Deletions != 1 {
		t.Fatalf("deleted = %+v", d)
	}
}

func TestLineMapper(t *testing.T) {
	// Two lines added after line 2, line 5 changed, line 8 removed.
	out := `diff --git a/f b/f
--- a/f
+++ b/f
@@ -2,0 +3,2 @@
+new a
+new b
@@ -5 +7 @@
-old five
+new five
@@ -8 +9,0 @@
-eight
`
	m := LineMapper(ParseDiff([]byte(out))[0].Hunks)
	for in, want := range map[int]int{1: 1, 2: 2, 3: 5, 4: 6, 6: 8, 7: 9, 9: 10, 20: 21} {
		if got, ok := m(in); !ok || got != want {
			t.Errorf("line %d -> %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []int{5, 8} {
		if _, ok := m(in); ok {
			t.Errorf("line %d should not map", in)
		}
	}
}
