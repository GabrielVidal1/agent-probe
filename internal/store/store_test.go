package store

import (
	"path/filepath"
	"testing"
)

func TestImportKeepsIDs(t *testing.T) {
	d := t.TempDir()
	old, err := Open(filepath.Join(d, "old.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_ = old.Put("call", "s", "k", map[string]int{"i": i})
	}
	_ = old.Put("usage", "", "u", map[string]int{})
	_, _ = old.db.Exec(`DELETE FROM records WHERE id = 2`) // a pruned record: a gap
	old.Close()
	st, err := Open(filepath.Join(d, "new.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := st.Import(filepath.Join(d, "old.db"), "usage")
	if err != nil || n != 4 {
		t.Fatal(n, err)
	}
	if st.LastID() != 6 { // the usage record left out was the last: a reader may have read it
		t.Fatal("last id", st.LastID())
	}
	_ = st.Put("call", "s", "k2", map[string]int{})
	if st.LastID() != 7 { // after the old ones (6 was the usage record's)
		t.Fatal("next id", st.LastID())
	}
	if _, err := st.Import(filepath.Join(d, "old.db")); err == nil {
		t.Fatal("imported into a store with records")
	}
}
