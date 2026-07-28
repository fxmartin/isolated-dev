package smoke

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsEveryUnusableRequestField(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	valid := Request{
		MachineName:      "isolated-dev-baseline-0f0f",
		BaseImageVersion: "baseline-0f0f",
		HomeDir:          home,
		FixtureDir:       filepath.Join(home, "baseline"),
		GuestUser:        "fx",
		CPUs:             2,
		MemoryGB:         4,
		HostPort:         18080,
		Marker:           "baseline-marker",
	}
	test := Test{
		Runner:       &runnerStub{},
		Machines:     &machineStub{},
		ImageEnsurer: &imageStub{},
		DockerWaiter: &dockerStub{},
		Address:      &addressStub{},
		Prober:       &proberStub{},
	}

	cases := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{
			name:   "machine name",
			mutate: func(request *Request) { request.MachineName = "-nope" },
			want:   "invalid machine name",
		},
		{
			name:   "base-image version",
			mutate: func(request *Request) { request.BaseImageVersion = "-nope" },
			want:   "invalid base-image version",
		},
		{
			name:   "relative home",
			mutate: func(request *Request) { request.HomeDir = "home" },
			want:   "home directory must be absolute",
		},
		{
			name:   "relative fixtures",
			mutate: func(request *Request) { request.FixtureDir = "baseline" },
			want:   "must be absolute",
		},
		{
			name:   "escaping fixtures",
			mutate: func(request *Request) { request.FixtureDir = filepath.Join(home, "..", "elsewhere") },
			want:   "outside the mounted home directory",
		},
		{
			name:   "fixtures at the home root",
			mutate: func(request *Request) { request.FixtureDir = home + string(filepath.Separator) },
			want:   "their own directory",
		},
		{
			name:   "guest user",
			mutate: func(request *Request) { request.GuestUser = "Root User" },
			want:   "invalid guest user name",
		},
		{
			name:   "marker",
			mutate: func(request *Request) { request.Marker = "two words" },
			want:   "invalid baseline marker",
		},
		{name: "CPUs", mutate: func(request *Request) { request.CPUs = 0 }, want: "CPUs must be positive"},
		{name: "memory", mutate: func(request *Request) { request.MemoryGB = 0 }, want: "memory must be positive"},
		{name: "port", mutate: func(request *Request) { request.HostPort = 0 }, want: "outside 1-65535"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			request := valid
			testCase.mutate(&request)
			_, err := test.validate(request)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("validate() error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}

	if _, err := test.validate(valid); err != nil {
		t.Fatalf("validate() error = %v for a valid request", err)
	}
}

func TestValidateReportsEveryMissingDependency(t *testing.T) {
	t.Parallel()

	complete := Test{
		Runner:       &runnerStub{},
		Machines:     &machineStub{},
		ImageEnsurer: &imageStub{},
		DockerWaiter: &dockerStub{},
		Address:      &addressStub{},
		Prober:       &proberStub{},
	}
	cases := map[string]func(*Test){
		"host command runner":      func(test *Test) { test.Runner = nil },
		"machine lifecycle":        func(test *Test) { test.Machines = nil },
		"base-image builder":       func(test *Test) { test.ImageEnsurer = nil },
		"Docker readiness waiter":  func(test *Test) { test.DockerWaiter = nil },
		"machine address resolver": func(test *Test) { test.Address = nil },
		"macOS HTTP prober":        func(test *Test) { test.Prober = nil },
	}
	for want, mutate := range cases {
		t.Run(want, func(t *testing.T) {
			t.Parallel()

			test := complete
			mutate(&test)
			_, err := test.validate(Request{})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("validate() error = %v, want it to name the missing %s", err, want)
			}
		})
	}
}

func TestRunFailsWhenTheBaseImageCannotBeBuilt(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.image.err = errors.New("no network")

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the failed image build reported")
	}
	// The image was never built, so teardown must not try to delete it.
	if harness.runner.index("image delete") != -1 {
		t.Error("Run() deleted a base image it never built")
	}
	if len(harness.machine.upRequests) != 0 {
		t.Error("Run() created a machine without a base image")
	}
	if _, err := os.Stat(harness.request.FixtureDir); !os.IsNotExist(err) {
		t.Errorf("Stat(fixtures) error = %v, want the fixtures removed", err)
	}
}

func TestRunFailsAndDiagnosesWhenTheMachineCannotBeCreated(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.machine.upErr = errors.New("Operation not supported by device")

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the failed machine creation reported")
	}
	if !strings.Contains(harness.logs.String(), "Operation not supported by device") {
		t.Errorf("diagnostics do not carry the failure:\n%s", harness.logs.String())
	}
	harness.runner.ran(t, "image delete")
}

func TestRunFailsWhenTheFixturesAreNotVisibleInsideTheGuest(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.request.GuestUser = ""
	inner := harness.runner.respond
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "test -f") {
			return nil, errors.New("No such file or directory")
		}
		return inner(call)
	}

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the missing home mount reported")
	}
	if !strings.Contains(err.Error(), "home mount is missing or incomplete") {
		t.Errorf("error = %v, want the mount named as the cause", err)
	}
}

func TestRunFailsWhenComposeCannotStart(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	inner := harness.runner.respond
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "up --detach --wait") {
			return []byte("pull access denied"), errors.New("exit status 1")
		}
		return inner(call)
	}

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the failed Compose start reported")
	}
	// Compose was attempted, so teardown removes whatever it left behind and
	// the diagnostics include its own state.
	harness.runner.ran(t, "down --remove-orphans")
	for _, want := range []string{"compose ps", "compose logs"} {
		if !strings.Contains(harness.logs.String(), want) {
			t.Errorf("diagnostics do not include %q:\n%s", want, harness.logs.String())
		}
	}
}

func TestRunReportsUnreadableNetworkState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		respond func(recordedCall) ([]byte, error)
		want    string
	}{
		{
			name: "driver unreadable",
			respond: func(call recordedCall) ([]byte, error) {
				if strings.Contains(call.line(), "{{.Driver}}") {
					return []byte("no such network"), errors.New("exit status 1")
				}
				return nil, nil
			},
			want: "inspect the private network",
		},
		{
			name: "count unreadable",
			respond: func(call recordedCall) ([]byte, error) {
				if strings.Contains(call.line(), "{{len .Containers}}") {
					return []byte("no such network"), errors.New("exit status 1")
				}
				return nil, nil
			},
			want: "count the containers",
		},
		{
			name: "count not a number",
			respond: func(call recordedCall) ([]byte, error) {
				if strings.Contains(call.line(), "{{len .Containers}}") {
					return []byte("<no value>\n"), nil
				}
				return nil, nil
			},
			want: "decode the container count",
		},
		{
			name: "one container attached",
			respond: func(call recordedCall) ([]byte, error) {
				if strings.Contains(call.line(), "{{len .Containers}}") {
					return []byte("1\n"), nil
				}
				return nil, nil
			},
			want: "carries 1 containers",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			inner := harness.runner.respond
			harness.runner.respond = func(call recordedCall) ([]byte, error) {
				output, err := testCase.respond(call)
				if output != nil || err != nil {
					return output, err
				}
				return inner(call)
			}

			_, err := harness.test.Run(context.Background(), harness.request)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Run() error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestRunReportsUnreadableServiceState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		respond func(recordedCall) ([]byte, error)
		want    string
	}{
		{
			name: "inspect fails",
			respond: func(recordedCall) ([]byte, error) {
				return []byte("No such object"), errors.New("exit status 1")
			},
			want: "inspect the baseline containers",
		},
		{
			name: "one container started",
			respond: func(recordedCall) ([]byte, error) {
				return []byte(OriginImage + " true\n"), nil
			},
			want: "started 1 containers",
		},
		{
			name: "unreadable line",
			respond: func(recordedCall) ([]byte, error) {
				return []byte(OriginImage + "\n" + ProxyImage + " true\n"), nil
			},
			want: "could not read the state of the origin container",
		},
		{
			name: "proxy not running",
			respond: func(recordedCall) ([]byte, error) {
				return []byte(OriginImage + " true\n" + ProxyImage + " false\n"), nil
			},
			want: "the proxy service is not running",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			inner := harness.runner.respond
			harness.runner.respond = func(call recordedCall) ([]byte, error) {
				if strings.Contains(call.line(), "docker inspect") {
					return testCase.respond(call)
				}
				return inner(call)
			}

			_, err := harness.test.Run(context.Background(), harness.request)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Run() error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestProbesRetryUntilThePublishedPortAnswers(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.test.ProbeTries = 0 // exercise the default budget
	harness.test.RetryDelay = time.Nanosecond

	guestAttempts := 0
	inner := harness.runner.respond
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "curl") {
			guestAttempts++
			if guestAttempts == 1 {
				return []byte("connection refused"), errors.New("exit status 7")
			}
		}
		return inner(call)
	}
	hostAttempts := 0
	harness.test.Prober = proberFunc(func(_ context.Context, _ string) (string, error) {
		hostAttempts++
		if hostAttempts == 1 {
			return "", errors.New("connection refused")
		}
		return harness.request.Marker + "\n", nil
	})

	if _, err := harness.test.Run(context.Background(), harness.request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if guestAttempts != 2 || hostAttempts != 2 {
		t.Errorf("attempts = %d guest / %d host, want one retry each", guestAttempts, hostAttempts)
	}
}

func TestProbesGiveUpAfterTheirBudget(t *testing.T) {
	t.Parallel()

	t.Run("guest", func(t *testing.T) {
		t.Parallel()

		harness := newHarness(t)
		harness.test.RetryDelay = time.Nanosecond
		inner := harness.runner.respond
		harness.runner.respond = func(call recordedCall) ([]byte, error) {
			if strings.Contains(call.line(), "curl") {
				return []byte("connection refused"), errors.New("exit status 7")
			}
			return inner(call)
		}

		_, err := harness.test.Run(context.Background(), harness.request)
		if err == nil || !strings.Contains(err.Error(), "inside the guest") {
			t.Fatalf("Run() error = %v, want the exhausted guest probe reported", err)
		}
	})

	t.Run("macOS", func(t *testing.T) {
		t.Parallel()

		harness := newHarness(t)
		harness.test.RetryDelay = time.Nanosecond
		harness.prober.err = errors.New("connection refused")

		_, err := harness.test.Run(context.Background(), harness.request)
		if err == nil || !strings.Contains(err.Error(), "from macOS") {
			t.Fatalf("Run() error = %v, want the exhausted macOS probe reported", err)
		}
	})
}

func TestProbeStopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.test.RetryDelay = time.Nanosecond
	ctx, cancel := context.WithCancel(context.Background())
	harness.prober.err = errors.New("connection refused")
	harness.test.Sleep = func(time.Duration) { cancel() }

	_, err := harness.test.Run(ctx, harness.request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want the cancelled context reported", err)
	}
	// Teardown still runs on a context of its own, which is the point of
	// detaching it.
	if len(harness.machine.destroyed) != 1 {
		t.Errorf("destroyed = %+v, want teardown to run after cancellation", harness.machine.destroyed)
	}
}

func TestProbeRetriesFallBackToTheDefaultWait(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Test){
		"default delay": func(test *Test) {
			test.RetryDelay = 0
			test.Sleep = func(time.Duration) {}
		},
		"default sleep": func(test *Test) {
			test.RetryDelay = time.Nanosecond
			test.Sleep = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			mutate(&harness.test)
			attempts := 0
			harness.test.Prober = proberFunc(func(context.Context, string) (string, error) {
				attempts++
				if attempts == 1 {
					return "", errors.New("connection refused")
				}
				return harness.request.Marker, nil
			})

			if _, err := harness.test.Run(context.Background(), harness.request); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if attempts != 2 {
				t.Errorf("attempts = %d, want one retry", attempts)
			}
		})
	}
}

func TestGuestProbeStopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	harness.test.Sleep = func(time.Duration) { cancel() }
	inner := harness.runner.respond
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "curl") {
			return []byte("connection refused"), errors.New("exit status 7")
		}
		return inner(call)
	}

	if _, err := harness.test.Run(ctx, harness.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want the cancelled context reported", err)
	}
}

func TestDiagnosticsRecordCommandsThatThemselvesFail(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.docker.err = errors.New("docker info did not succeed")
	inner := harness.runner.respond
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		if strings.Contains(call.line(), "systemctl is-system-running") {
			return []byte("offline"), errors.New("exit status 1")
		}
		return inner(call)
	}

	if _, err := harness.test.Run(context.Background(), harness.request); err == nil {
		t.Fatal("Run() error = nil, want the unready daemon reported")
	}
	// A diagnostic that fails is itself a finding: the known Apple Container
	// 1.1.0 race leaves systemd unavailable, and that is the answer.
	if !strings.Contains(harness.logs.String(), "(command failed:") {
		t.Errorf("diagnostics do not record the failing command:\n%s", harness.logs.String())
	}
}

func TestRunReportsFixturesItCannotWrite(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	if err := os.Chmod(harness.request.HomeDir, 0o555); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(harness.request.HomeDir, 0o755); err != nil {
			t.Errorf("restore %s: %v", harness.request.HomeDir, err)
		}
	})

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil || !strings.Contains(err.Error(), "create baseline fixture directory") {
		t.Fatalf("Run() error = %v, want the unwritable fixture directory reported", err)
	}
	if len(harness.image.references) != 0 {
		t.Error("Run() built a base image before it could write its fixtures")
	}
}

func TestRunFailsWhenTheMachineAddressCannotBeResolved(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.test.Address = &addressStub{err: errors.New("no reachable IPv4 address")}

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil || !strings.Contains(err.Error(), "no reachable IPv4 address") {
		t.Fatalf("Run() error = %v, want the unresolved address reported", err)
	}
}

func TestRunReportsEveryTeardownFailure(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.machine.destroyErr = errors.New("machine is busy")
	inner := harness.runner.respond
	harness.runner.respond = func(call recordedCall) ([]byte, error) {
		line := call.line()
		if strings.Contains(line, "down --remove-orphans") {
			return []byte("daemon gone"), errors.New("exit status 1")
		}
		if strings.Contains(line, "image delete") {
			return []byte("image is in use"), errors.New("exit status 1")
		}
		return inner(call)
	}

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil {
		t.Fatal("Run() error = nil, want the leaked resources reported")
	}
	for _, want := range []string{
		"remove the baseline containers and network",
		"delete the baseline machine",
		"delete the baseline base image",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to report %q", err, want)
		}
	}
}

func TestTeardownReportsFixturesItCannotRemove(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	// A fixture directory whose parent denies writes cannot be removed, which
	// is exactly the leak teardown has to surface rather than swallow.
	parent := filepath.Dir(harness.request.FixtureDir)
	if err := os.MkdirAll(harness.request.FixtureDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Errorf("restore %s: %v", parent, err)
		}
	})

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil || !strings.Contains(err.Error(), "remove baseline fixtures") {
		t.Fatalf("Run() error = %v, want the undeletable fixtures reported", err)
	}
}

func TestDiagnoseStaysSilentWithoutADestination(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.test.Diagnostics = nil
	harness.docker.err = errors.New("docker info did not succeed")

	_, err := harness.test.Run(context.Background(), harness.request)
	if err == nil || !strings.Contains(err.Error(), "docker info did not succeed") {
		t.Fatalf("Run() error = %v, want the unwrapped cause returned", err)
	}
	if harness.runner.index("systemctl is-system-running") != -1 {
		t.Error("diagnostics ran without a destination to write them to")
	}
}

func TestComposeProjectNameIsAcceptableToCompose(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"isolated-dev-baseline-0f0f": "isolated-dev-baseline-0f0f",
		"Isolated.Dev_1":             "isolated-dev_1",
		"--leading":                  "leading",
	}
	for machineName, want := range cases {
		if got := composeProjectName(machineName); got != want {
			t.Errorf("composeProjectName(%q) = %q, want %q", machineName, got, want)
		}
		if !composeProjectNamePattern.MatchString(want) {
			t.Errorf("%q is not an acceptable Compose project name", want)
		}
	}
}

func TestHTTPProberReadsTheMarkerAndReportsFailures(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/"+MarkerFileName {
			writer.Write([]byte("baseline-marker\n"))
			return
		}
		http.Error(writer, "not found", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	prober := HTTPProber{}
	body, err := prober.Get(context.Background(), server.URL+"/"+MarkerFileName)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if strings.TrimSpace(body) != "baseline-marker" {
		t.Errorf("body = %q", body)
	}

	if _, err := prober.Get(context.Background(), server.URL+"/absent"); err == nil ||
		!strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("Get(absent) error = %v, want the status reported", err)
	}
	if _, err := prober.Get(context.Background(), "http://%zz"); err == nil {
		t.Error("Get(malformed) error = nil, want the unusable URL reported")
	}

	// A response that is cut off mid-body must be reported rather than read as
	// a shorter marker.
	truncating := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "64")
		writer.Write([]byte("baseline"))
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(truncating.Close)
	if _, err := prober.Get(context.Background(), truncating.URL+"/"+MarkerFileName); err == nil {
		t.Error("Get(truncated) error = nil, want the incomplete body reported")
	}

	server.Close()
	if _, err := (HTTPProber{Client: server.Client()}).Get(
		context.Background(),
		server.URL+"/"+MarkerFileName,
	); err == nil {
		t.Error("Get(closed) error = nil, want the failed request reported")
	}
}

func TestWriteFixtureRejectsUnusableInputs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cases := []struct {
		name    string
		dir     string
		project string
		marker  string
		port    int
	}{
		{name: "relative directory", dir: "baseline", project: "baseline", marker: "m", port: 18080},
		{name: "empty marker", dir: filepath.Join(root, "a"), project: "baseline", marker: "", port: 18080},
		{name: "port out of range", dir: filepath.Join(root, "b"), project: "baseline", marker: "m", port: 70000},
		{name: "directory under a file", dir: filepath.Join(file, "c"), project: "baseline", marker: "m", port: 18080},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if _, err := WriteFixture(
				testCase.dir,
				testCase.project,
				testCase.marker,
				testCase.port,
			); err == nil {
				t.Fatal("WriteFixture() error = nil, want the unusable input rejected")
			}
		})
	}
}

func TestWriteFixtureReportsFilesItCannotWrite(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "baseline")
	if err := os.MkdirAll(dir, 0o555); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Errorf("restore %s: %v", dir, err)
		}
	})

	if _, err := WriteFixture(dir, "baseline", "marker", 18080); err == nil {
		t.Fatal("WriteFixture() error = nil, want the unwritable fixture reported")
	}
}

func TestFixtureRemoveIgnoresAnUnsetDirectory(t *testing.T) {
	t.Parallel()

	if err := (Fixture{}).Remove(); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
}

// proberFunc adapts a function to Prober for the retry tests.
type proberFunc func(context.Context, string) (string, error)

func (probe proberFunc) Get(ctx context.Context, url string) (string, error) {
	return probe(ctx, url)
}
