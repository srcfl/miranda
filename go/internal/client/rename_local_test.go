// go/internal/client/rename_local_test.go — N1: the name merge is last-writer-
// wins on NameTS, and local renames persist keyed by machine_id.
package client

import (
	"testing"
)

func TestMergeMachinesNameLastWriterWins(t *testing.T) {
	const id = "m1"
	cases := []struct {
		name       string
		local      Machine
		discovered Machine
		want       string
	}{
		{
			// Legacy local entry (no NameTS): the registry record is canonical,
			// so a rename made elsewhere propagates here.
			name:       "registry wins over legacy local",
			local:      Machine{MachineID: id, Name: "old"},
			discovered: Machine{MachineID: id, Name: "renamed", NameTS: 100},
			want:       "renamed",
		},
		{
			// A local rename not yet delivered to the machine keeps winning.
			name:       "newer local rename wins",
			local:      Machine{MachineID: id, Name: "mine", NameTS: 200},
			discovered: Machine{MachineID: id, Name: "stale", NameTS: 100},
			want:       "mine",
		},
		{
			// Once the machine republishes (same or newer ts), registry wins —
			// the renaming device converges on its own seal's ts.
			name:       "equal ts converges on registry",
			local:      Machine{MachineID: id, Name: "renamed", NameTS: 100},
			discovered: Machine{MachineID: id, Name: "renamed", NameTS: 100},
			want:       "renamed",
		},
		{
			name:       "empty registry name keeps local",
			local:      Machine{MachineID: id, Name: "kept", NameTS: 5},
			discovered: Machine{MachineID: id, Name: "", NameTS: 999},
			want:       "kept",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			merged := MergeMachines([]Machine{c.local}, []Machine{c.discovered})
			if len(merged) != 1 || merged[0].Name != c.want {
				t.Fatalf("merged = %+v, want name %q", merged, c.want)
			}
		})
	}
}

func TestRenameLocalMachineUpserts(t *testing.T) {
	dir := t.TempDir()
	existing := Machine{Name: "old", MachineID: "m1", HostPubHex: "aa", SignalURL: "https://r"}
	if err := AddMachine(dir, existing); err != nil {
		t.Fatal(err)
	}

	if err := RenameLocalMachine(dir, existing, "new", 123); err != nil {
		t.Fatal(err)
	}
	list, err := ListMachines(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "new" || list[0].NameTS != 123 || list[0].HostPubHex != "aa" {
		t.Fatalf("renamed entry = %+v", list)
	}

	// A machine known only from the registry gets added, pin and all, so the
	// rename survives the machine going offline (registry rows vanish then).
	discovered := Machine{Name: "reg", MachineID: "m2", HostPubHex: "bb", SignalURL: "https://r"}
	if err := RenameLocalMachine(dir, discovered, "reg2", 456); err != nil {
		t.Fatal(err)
	}
	list, err = ListMachines(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[1].MachineID != "m2" || list[1].Name != "reg2" || list[1].NameTS != 456 || list[1].HostPubHex != "bb" {
		t.Fatalf("upserted entry = %+v", list)
	}
}
