package forge

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The persistence run reaches its decisions through a handful of small pure
// helpers: what makes a volume "the same volume", what a measured interval says
// about its target, and what a marker name is allowed to be. Driving them
// through Validate proves they are wired in; driving them directly is what
// pins down the boundary values a whole-run test only visits by accident.

func TestDescribeVolumeDifferenceNamesWhatTheRestartLost(t *testing.T) {
	const mountpoint = "/var/lib/docker/volumes/rosetta-db-dev/_data"
	before := VolumeIdentity{
		Driver:     "local",
		Mountpoint: mountpoint,
		CreatedAt:  testCreatedAt,
		Entries:    []string{"PG_VERSION", "base", "pg_wal"},
	}

	tests := map[string]struct {
		after VolumeIdentity
		want  string
	}{
		"the same volume with the same data": {
			after: before,
			want:  "",
		},
		"a recreated volume": {
			after: VolumeIdentity{
				Driver:     "local",
				Mountpoint: mountpoint,
				CreatedAt:  "2026-07-28T11:00:00+02:00",
				Entries:    before.Entries,
			},
			want: "it was recreated",
		},
		"a different driver": {
			after: VolumeIdentity{
				Driver:     "overlay",
				Mountpoint: mountpoint,
				CreatedAt:  testCreatedAt,
				Entries:    before.Entries,
			},
			want: "it is now a different volume",
		},
		"a different mountpoint": {
			after: VolumeIdentity{
				Driver:     "local",
				Mountpoint: "/var/lib/docker/volumes/rosetta-db-dev-2/_data",
				CreatedAt:  testCreatedAt,
				Entries:    before.Entries,
			},
			want: "it is now a different volume",
		},
		"one lost entry": {
			after: VolumeIdentity{
				Driver:     "local",
				Mountpoint: mountpoint,
				CreatedAt:  testCreatedAt,
				Entries:    []string{"PG_VERSION", "base"},
			},
			want: "it no longer holds pg_wal",
		},
		"several lost entries are all named": {
			after: VolumeIdentity{
				Driver:     "local",
				Mountpoint: mountpoint,
				CreatedAt:  testCreatedAt,
				Entries:    []string{"PG_VERSION"},
			},
			want: "it no longer holds base, pg_wal",
		},
		"an emptied volume": {
			after: VolumeIdentity{
				Driver:     "local",
				Mountpoint: mountpoint,
				CreatedAt:  testCreatedAt,
				Entries:    nil,
			},
			want: "it no longer holds PG_VERSION, base, pg_wal",
		},
		"new data alongside the old": {
			after: VolumeIdentity{
				Driver:     "local",
				Mountpoint: mountpoint,
				CreatedAt:  testCreatedAt,
				Entries:    []string{"PG_VERSION", "base", "pg_stat", "pg_wal"},
			},
			want: "",
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			got := describeVolumeDifference(before, testCase.after)
			if testCase.want == "" {
				if got != "" {
					t.Fatalf("describeVolumeDifference() = %q, want the volume accepted", got)
				}
				return
			}
			if !strings.Contains(got, testCase.want) {
				t.Fatalf("describeVolumeDifference() = %q, want it to contain %q", got, testCase.want)
			}
		})
	}
}

// A recreated volume is reported ahead of a moved one, because losing the data
// is the more useful thing to tell the developer about the same volume.
func TestDescribeVolumeDifferenceReportsRecreationBeforeRelocation(t *testing.T) {
	before := VolumeIdentity{Driver: "local", Mountpoint: "/old", CreatedAt: testCreatedAt}
	after := VolumeIdentity{Driver: "overlay", Mountpoint: "/new", CreatedAt: "2026-07-28T11:00:00+02:00"}

	if got := describeVolumeDifference(before, after); !strings.Contains(got, "it was recreated") {
		t.Fatalf("describeVolumeDifference() = %q, want the recreation reported first", got)
	}
}

// compareVolumes still describes every volume it was given, so a report from a
// failed run shows which volumes did survive alongside the one that did not.
func TestCompareVolumesDescribesEveryVolumeAndFailsOnTheFirstLoss(t *testing.T) {
	volumes := DevVolumes()
	before := []VolumeIdentity{
		{Driver: "local", Mountpoint: "/db", CreatedAt: testCreatedAt, Entries: []string{"base"}},
		{Driver: "local", Mountpoint: "/data", CreatedAt: testCreatedAt, Entries: []string{"uploads"}},
	}
	after := []VolumeIdentity{
		{Driver: "local", Mountpoint: "/db", CreatedAt: testCreatedAt, Entries: nil},
		{Driver: "local", Mountpoint: "/data", CreatedAt: testCreatedAt, Entries: nil},
	}

	states, err := compareVolumes(volumes, before, after)
	if err == nil || !strings.Contains(err.Error(), volumes[0].Name) {
		t.Fatalf("compareVolumes() error = %v, want the first lost volume reported", err)
	}
	if !strings.Contains(err.Error(), volumes[0].Description) {
		t.Fatalf("compareVolumes() error = %v, want it to describe the data that was lost", err)
	}
	if len(states) != len(volumes) {
		t.Fatalf("compareVolumes() returned %d states, want %d", len(states), len(volumes))
	}
	for index, state := range states {
		if state.Preserved {
			t.Errorf("state[%d].Preserved = true, want the loss recorded", index)
		}
		if state.Difference == "" {
			t.Errorf("state[%d].Difference is empty, want it to explain the loss", index)
		}
	}
}

func TestCompareVolumesAcceptsAPreservedRestart(t *testing.T) {
	volumes := DevVolumes()
	identities := []VolumeIdentity{
		{Driver: "local", Mountpoint: "/db", CreatedAt: testCreatedAt, Entries: []string{"base"}},
		{Driver: "local", Mountpoint: "/data", CreatedAt: testCreatedAt, Entries: []string{"uploads"}},
	}

	states, err := compareVolumes(volumes, identities, identities)
	if err != nil {
		t.Fatalf("compareVolumes() error = %v, want the restart accepted", err)
	}
	for index, state := range states {
		if !state.Preserved || state.Difference != "" {
			t.Errorf("state[%d] = %+v, want it preserved without a difference", index, state)
		}
	}
}

// The targets are the acceptance criteria's own numbers, so a measurement that
// lands exactly on one has to count as met rather than missed.
func TestTimingTreatsTheTargetAsInclusive(t *testing.T) {
	tests := map[string]struct {
		timing  Timing
		met     bool
		outcome string
	}{
		"comfortably inside the target": {
			timing:  Timing{Label: "machine ready", Elapsed: 12 * time.Second, Target: MachineReadyTarget},
			met:     true,
			outcome: "met",
		},
		"exactly on the target": {
			timing:  Timing{Label: "machine ready", Elapsed: MachineReadyTarget, Target: MachineReadyTarget},
			met:     true,
			outcome: "met",
		},
		"a nanosecond over the target": {
			timing:  Timing{Label: "machine ready", Elapsed: MachineReadyTarget + time.Nanosecond, Target: MachineReadyTarget},
			met:     false,
			outcome: "missed",
		},
		"well past the target": {
			timing:  Timing{Label: "stack ready", Elapsed: 5 * time.Minute, Target: StackReadyTarget},
			met:     false,
			outcome: "missed",
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			if got := testCase.timing.Met(); got != testCase.met {
				t.Fatalf("Met() = %t, want %t", got, testCase.met)
			}
			rendered := testCase.timing.String()
			if !strings.Contains(rendered, "("+testCase.outcome+")") {
				t.Fatalf("String() = %q, want it to report %q", rendered, testCase.outcome)
			}
			if !strings.Contains(rendered, testCase.timing.Label) {
				t.Fatalf("String() = %q, want it to name %q", rendered, testCase.timing.Label)
			}
			if !strings.Contains(rendered, testCase.timing.Target.String()) {
				t.Fatalf("String() = %q, want it to state the %s target", rendered, testCase.timing.Target)
			}
		})
	}
}

// Sub-millisecond noise is not what a 30-second target is about, so the report
// rounds it away rather than printing a spurious tail.
func TestTimingRoundsTheElapsedTimeToMilliseconds(t *testing.T) {
	timing := Timing{
		Label:   "machine ready",
		Elapsed: 12*time.Second + 340*time.Millisecond + 678*time.Microsecond,
		Target:  MachineReadyTarget,
	}

	if got := timing.String(); !strings.Contains(got, "12.341s") {
		t.Fatalf("String() = %q, want the elapsed time rounded to 12.341s", got)
	}
}

func TestMissedTargetsReportsOnlyTheMeasurementsThatExceededTheirTarget(t *testing.T) {
	tests := map[string]struct {
		timings []Timing
		want    []string
	}{
		"nothing measured": {
			timings: nil,
			want:    nil,
		},
		"every target met": {
			timings: []Timing{
				{Label: "machine ready", Elapsed: time.Second, Target: MachineReadyTarget},
				{Label: "stack ready", Elapsed: time.Minute, Target: StackReadyTarget},
			},
			want: nil,
		},
		"one target missed": {
			timings: []Timing{
				{Label: "machine ready", Elapsed: time.Minute, Target: MachineReadyTarget},
				{Label: "stack ready", Elapsed: time.Minute, Target: StackReadyTarget},
			},
			want: []string{"machine ready"},
		},
		"both targets missed": {
			timings: []Timing{
				{Label: "machine ready", Elapsed: time.Minute, Target: MachineReadyTarget},
				{Label: "stack ready", Elapsed: time.Hour, Target: StackReadyTarget},
			},
			want: []string{"machine ready", "stack ready"},
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			missed := PersistenceReport{Timings: testCase.timings}.MissedTargets()
			var labels []string
			for _, timing := range missed {
				labels = append(labels, timing.Label)
			}
			if !slices.Equal(labels, testCase.want) {
				t.Fatalf("MissedTargets() = %v, want %v", labels, testCase.want)
			}
		})
	}
}

// The marker is the only thing a persistence run writes into the acceptance
// workload, so its name has to stay a single file directly inside the project.
func TestValidateMarkerNameKeepsTheMarkerInsideTheProject(t *testing.T) {
	tests := map[string]struct {
		name     string
		accepted bool
	}{
		"the default name":         {name: DefaultMarkerName, accepted: true},
		"an ordinary file name":    {name: "scratch.txt", accepted: true},
		"a relative path":          {name: "src/scratch.txt"},
		"an absolute path":         {name: "/etc/passwd"},
		"a parent traversal":       {name: "../scratch.txt"},
		"the parent directory":     {name: ".."},
		"the project itself":       {name: "."},
		"an empty name":            {name: ""},
		"a whitespace-only name":   {name: "   "},
		"a trailing separator":     {name: "scratch/"},
		"a nested home reference":  {name: "~/.ssh/id_ed25519"},
		"a deep traversal upwards": {name: "../../.ssh/id_ed25519"},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateMarkerName(testCase.name)
			if testCase.accepted {
				if err != nil {
					t.Fatalf("validateMarkerName(%q) error = %v, want it accepted", testCase.name, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateMarkerName(%q) = nil, want the name refused", testCase.name)
			}
			if !strings.Contains(err.Error(), "single file name") {
				t.Fatalf("validateMarkerName(%q) error = %v, want it to say why", testCase.name, err)
			}
		})
	}
}

// The marker names the machine it belongs to, so a stray file left behind by a
// crashed run says which environment wrote it.
func TestMarkerContentNamesTheMachine(t *testing.T) {
	content := markerContent("isolated-dev-forge")

	if !strings.Contains(content, "isolated-dev-forge") {
		t.Fatalf("markerContent() = %q, want it to name the machine", content)
	}
	if !strings.HasSuffix(content, "\n") {
		t.Fatalf("markerContent() = %q, want it to end with a newline", content)
	}
}

func TestParseOwnershipReadsTheNumericIdentityStatReports(t *testing.T) {
	tests := map[string]struct {
		value string
		uid   int
		gid   int
		valid bool
	}{
		"an ordinary identity":  {value: "501:20", uid: 501, gid: 20, valid: true},
		"root":                  {value: "0:0", uid: 0, gid: 0, valid: true},
		"no separator":          {value: "501 20"},
		"an empty value":        {value: ""},
		"a missing gid":         {value: "501:"},
		"a missing uid":         {value: ":20"},
		"a non-numeric uid":     {value: "fxmartin:20"},
		"a non-numeric gid":     {value: "501:staff"},
		"a third field":         {value: "501:20:0"},
		"a padded value":        {value: "501 : 20"},
		"a negative identity":   {value: "-1:-1", uid: -1, gid: -1, valid: true},
		"a stat error instead":  {value: "stat: cannot stat"},
		"a hexadecimal attempt": {value: "0x1f5:0x14"},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			uid, gid, err := parseOwnership(testCase.value)
			if !testCase.valid {
				if err == nil {
					t.Fatalf("parseOwnership(%q) = %d:%d, want it refused", testCase.value, uid, gid)
				}
				if !strings.Contains(err.Error(), "want uid:gid") {
					t.Fatalf("parseOwnership(%q) error = %v, want it to state the shape", testCase.value, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOwnership(%q) error = %v, want it parsed", testCase.value, err)
			}
			if uid != testCase.uid || gid != testCase.gid {
				t.Fatalf("parseOwnership(%q) = %d:%d, want %d:%d", testCase.value, uid, gid, testCase.uid, testCase.gid)
			}
		})
	}
}

// An unset request still checks the volumes the DEV profile declares, which is
// what makes `Validate` usable without repeating the Forge topology.
func TestPersistenceRequestDefaultsToTheDevProfile(t *testing.T) {
	filled := PersistenceRequest{}.withDefaults()

	if !slices.Equal(filled.Volumes, DevVolumes()) {
		t.Errorf("Volumes = %v, want the DEV profile volumes %v", filled.Volumes, DevVolumes())
	}
	if filled.MarkerName != DefaultMarkerName {
		t.Errorf("MarkerName = %q, want %q", filled.MarkerName, DefaultMarkerName)
	}
}

func TestPersistenceRequestKeepsWhatTheCallerChose(t *testing.T) {
	chosen := []Volume{{Name: "rosetta-cache-dev", Description: "the build cache"}}
	filled := PersistenceRequest{Volumes: chosen, MarkerName: "scratch.txt"}.withDefaults()

	if !slices.Equal(filled.Volumes, chosen) {
		t.Errorf("Volumes = %v, want the caller's %v", filled.Volumes, chosen)
	}
	if filled.MarkerName != "scratch.txt" {
		t.Errorf("MarkerName = %q, want the caller's scratch.txt", filled.MarkerName)
	}
}

// An empty volume list is a caller mistake rather than a default, because
// checking nothing would report a persistence run that proved nothing.
func TestPersistenceRequestDoesNotRestoreAnExplicitlyEmptyVolumeList(t *testing.T) {
	filled := PersistenceRequest{Volumes: []Volume{}}.withDefaults()

	if len(filled.Volumes) != 0 {
		t.Fatalf("Volumes = %v, want the explicitly empty list kept for validation to refuse", filled.Volumes)
	}
}

func TestClosureTriesFallsBackWhenTheCallerGivesNoBound(t *testing.T) {
	tests := map[string]struct {
		configured int
		want       int
	}{
		"unset":      {configured: 0, want: 15},
		"negative":   {configured: -3, want: 15},
		"one try":    {configured: 1, want: 1},
		"configured": {configured: 40, want: 40},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			got := Persistence{ClosureTries: testCase.configured}.closureTries()
			if got != testCase.want {
				t.Fatalf("closureTries() = %d, want %d", got, testCase.want)
			}
		})
	}
}

// writeMarker refuses rather than overwrites, because the marker lands inside a
// real repository whose files isolated-dev does not own.
func TestWriteMarkerCreatesTheMarkerOnlyWhenNothingIsThere(t *testing.T) {
	project := t.TempDir()
	marker := filepath.Join(project, DefaultMarkerName)
	content := markerContent("isolated-dev-forge")

	if err := writeMarker(marker, content); err != nil {
		t.Fatalf("writeMarker() error = %v, want the marker created", err)
	}
	written, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ReadFile() error = %v, want the marker readable", err)
	}
	if string(written) != content {
		t.Fatalf("marker content = %q, want %q", written, content)
	}

	err = writeMarker(marker, content)
	if err == nil {
		t.Fatal("writeMarker() = nil on an existing file, want it refused")
	}
	if !strings.Contains(err.Error(), DefaultMarkerName) {
		t.Errorf("writeMarker() error = %v, want it to name the file in the way", err)
	}
	if !strings.Contains(err.Error(), "another marker name") {
		t.Errorf("writeMarker() error = %v, want it to suggest a way out", err)
	}
	if again, readErr := os.ReadFile(marker); readErr != nil || string(again) != content {
		t.Errorf("marker content = %q (err %v), want the existing file untouched", again, readErr)
	}
}

// reserveMarker is the guard the guest copy has instead of O_EXCL: the guest
// creates it with `cp`, which overwrites, so the name has to be checked before
// the run writes anything.
func TestReserveMarkerRefusesANameThatIsTaken(t *testing.T) {
	guestCopy := filepath.Join(t.TempDir(), DefaultMarkerName+guestCopySuffix)

	if err := reserveMarker(guestCopy); err != nil {
		t.Fatalf("reserveMarker() error = %v, want a free name accepted", err)
	}
	if _, err := os.Stat(guestCopy); !os.IsNotExist(err) {
		t.Errorf("Stat() error = %v, want the check to have written nothing", err)
	}

	if err := os.WriteFile(guestCopy, []byte("real content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	err := reserveMarker(guestCopy)
	if err == nil {
		t.Fatal("reserveMarker() = nil on an existing file, want it refused")
	}
	if !strings.Contains(err.Error(), DefaultMarkerName+guestCopySuffix) {
		t.Errorf("reserveMarker() error = %v, want it to name the file in the way", err)
	}
	if !strings.Contains(err.Error(), "another marker name") {
		t.Errorf("reserveMarker() error = %v, want it to suggest a way out", err)
	}
	if data, readErr := os.ReadFile(guestCopy); readErr != nil || string(data) != "real content\n" {
		t.Errorf("file = %q (%v), want the existing file untouched", data, readErr)
	}
}

// A name that cannot even be looked at is refused rather than assumed free.
func TestReserveMarkerReportsANameItCannotCheck(t *testing.T) {
	notADirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADirectory, nil, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := reserveMarker(filepath.Join(notADirectory, DefaultMarkerName+guestCopySuffix))
	if err == nil {
		t.Fatal("reserveMarker() = nil, want the uncheckable name reported")
	}
	if !strings.Contains(err.Error(), DefaultMarkerName+guestCopySuffix) {
		t.Errorf("reserveMarker() error = %v, want it to name the marker", err)
	}
}

func TestWriteMarkerReportsAProjectItCannotWriteInto(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-directory", DefaultMarkerName)

	err := writeMarker(missing, markerContent("isolated-dev-forge"))
	if err == nil {
		t.Fatal("writeMarker() = nil, want the unwritable path reported")
	}
	if !strings.Contains(err.Error(), DefaultMarkerName) {
		t.Errorf("writeMarker() error = %v, want it to name the marker", err)
	}
}

// readHostCopy is the macOS half of the round trip: the file Linux created has
// to hold what Linux wrote and belong to the developer running the CLI.
func TestReadHostCopyAcceptsAGuestFileMacOSCanUse(t *testing.T) {
	hostCopy := filepath.Join(t.TempDir(), DefaultMarkerName+guestCopySuffix)
	content := markerContent("isolated-dev-forge")
	if err := os.WriteFile(hostCopy, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var roundTrip EditRoundTrip
	if err := readHostCopy(hostCopy, content, &roundTrip); err != nil {
		t.Fatalf("readHostCopy() error = %v, want the guest file accepted", err)
	}
	if !roundTrip.GuestFileRead {
		t.Error("GuestFileRead = false, want macOS reading the guest file recorded")
	}
	if roundTrip.HostUID != os.Getuid() || roundTrip.HostGID != os.Getgid() {
		t.Errorf(
			"host ownership = %d:%d, want the running developer's %d:%d",
			roundTrip.HostUID,
			roundTrip.HostGID,
			os.Getuid(),
			os.Getgid(),
		)
	}
}

// Trailing whitespace is an artifact of shell redirection rather than a
// difference in what the two sides of the mount hold.
func TestReadHostCopyIgnoresSurroundingWhitespace(t *testing.T) {
	hostCopy := filepath.Join(t.TempDir(), DefaultMarkerName+guestCopySuffix)
	content := markerContent("isolated-dev-forge")
	if err := os.WriteFile(hostCopy, []byte("\n  "+strings.TrimSpace(content)+"  \n\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var roundTrip EditRoundTrip
	if err := readHostCopy(hostCopy, content, &roundTrip); err != nil {
		t.Fatalf("readHostCopy() error = %v, want the guest file accepted", err)
	}
	if !roundTrip.GuestFileRead {
		t.Error("GuestFileRead = false, want the guest file accepted")
	}
}

func TestReadHostCopyReportsWhatMacOSCouldNotConfirm(t *testing.T) {
	content := markerContent("isolated-dev-forge")

	tests := map[string]struct {
		write func(t *testing.T, hostCopy string)
		want  string
	}{
		"the guest file never reached macOS": {
			write: func(*testing.T, string) {},
			want:  "read the file Linux created back from macOS",
		},
		"macOS sees different content": {
			write: func(t *testing.T, hostCopy string) {
				if err := os.WriteFile(hostCopy, []byte("a stale mount\n"), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			},
			want: "a stale mount",
		},
		"macOS sees an empty file": {
			write: func(t *testing.T, hostCopy string) {
				if err := os.WriteFile(hostCopy, nil, 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			},
			want: strings.TrimSpace(content),
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			hostCopy := filepath.Join(t.TempDir(), DefaultMarkerName+guestCopySuffix)
			testCase.write(t, hostCopy)

			var roundTrip EditRoundTrip
			err := readHostCopy(hostCopy, content, &roundTrip)
			if err == nil {
				t.Fatalf("readHostCopy() = nil, want the failure reported")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("readHostCopy() error = %v, want it to contain %q", err, testCase.want)
			}
			if roundTrip.GuestFileRead {
				t.Error("GuestFileRead = true, want the unconfirmed round trip left false")
			}
		})
	}
}

// DevVolumes hands out a fresh slice, so a caller trimming its own request
// cannot quietly change what every later run checks.
func TestDevVolumesReturnsAnIndependentList(t *testing.T) {
	first := DevVolumes()
	first[0].Name = "mutated"

	second := DevVolumes()
	if second[0].Name == "mutated" {
		t.Fatal("DevVolumes() returned a shared slice, want a fresh list on every call")
	}
	if len(second) != 2 {
		t.Fatalf("DevVolumes() returned %d volumes, want the database and the application data", len(second))
	}
}
