package taskrunner

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/client/allocrunner/taskrunner/interfaces"
	"github.com/hashicorp/nomad/client/taskenv"
	"github.com/hashicorp/nomad/helper/testlog"
	"github.com/hashicorp/nomad/nomad/structs"
	"github.com/stretchr/testify/require"
)

func newScriptMock(hb heartbeater, exec interfaces.ScriptExecutor, logger hclog.Logger, interval, timeout time.Duration) *scriptCheck {
	script := newScriptCheck(&scriptCheckConfig{
		allocID:   "allocid",
		taskName:  "testtask",
		serviceID: "serviceid",
		check: &structs.ServiceCheck{
			Interval: interval,
			Timeout:  timeout,
		},
		agent:      hb,
		driverExec: exec,
		taskEnv:    &taskenv.TaskEnv{},
		logger:     logger,
		shutdownCh: nil,
	})
	script.callback = newScriptCheckCallback(script)
	script.lastCheckOk = true
	return script
}

// fakeHeartbeater implements the heartbeater interface to allow mocking out
// Consul in script executor tests.
type fakeHeartbeater struct {
	heartbeats chan heartbeat
}

func (f *fakeHeartbeater) UpdateTTL(checkID, output, status string) error {
	f.heartbeats <- heartbeat{checkID: checkID, output: output, status: status}
	return nil
}

func newFakeHeartbeater() *fakeHeartbeater {
	return &fakeHeartbeater{heartbeats: make(chan heartbeat)}
}

type heartbeat struct {
	checkID string
	output  string
	status  string
}

// TestScript_Exec_Cancel asserts cancelling a script check shortcircuits
// any running scripts.
func TestScript_Exec_Cancel(t *testing.T) {
	exec, cancel := newBlockingScriptExec()
	defer cancel()

	logger := testlog.HCLogger(t)
	script := newScriptMock(nil, // heartbeater should never be called
		exec, logger, time.Hour, time.Hour)

	handle := script.run()
	<-exec.running  // wait until Exec is called
	handle.cancel() // cancel now that we're blocked in exec

	select {
	case <-handle.wait():
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for script check to exit")
	}

	// The underlying ScriptExecutor (newBlockScriptExec) *cannot* be
	// canceled. Only a wrapper around it obeys the context cancelation.
	require.NotEqual(t, atomic.LoadInt32(&exec.exited), 1,
		"expected script executor to still be running after timeout")
}

// TestScript_Exec_TimeoutBasic asserts a script will be killed when the
// timeout is reached.
func TestScript_Exec_TimeoutBasic(t *testing.T) {
	t.Parallel()
	exec, cancel := newBlockingScriptExec()
	defer cancel()

	logger := testlog.HCLogger(t)
	hb := newFakeHeartbeater()
	script := newScriptMock(hb, exec, logger, time.Hour, time.Second)

	handle := script.run()
	defer handle.cancel() // cleanup
	<-exec.running        // wait until Exec is called

	// Check for UpdateTTL call
	select {
	case update := <-hb.heartbeats:
		require.Equal(t, update.output, context.DeadlineExceeded.Error())
		require.Equal(t, update.status, api.HealthCritical)
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for script check to exit")
	}

	// The underlying ScriptExecutor (newBlockScriptExec) *cannot* be
	// canceled. Only a wrapper around it obeys the context cancelation.
	require.NotEqual(t, atomic.LoadInt32(&exec.exited), 1,
		"expected script executor to still be running after timeout")

	// Cancel and watch for exit
	handle.cancel()
	select {
	case <-handle.wait(): // ok!
	case update := <-hb.heartbeats:
		t.Errorf("unexpected UpdateTTL call on exit with status=%q", update)
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for script check to exit")
	}
}

// TestScript_Exec_TimeoutCritical asserts a script will be killed when
// the timeout is reached and always set a critical status regardless of what
// Exec returns.
func TestScript_Exec_TimeoutCritical(t *testing.T) {
	t.Parallel()
	logger := testlog.HCLogger(t)
	hb := newFakeHeartbeater()
	script := newScriptMock(hb, sleeperExec{}, logger, time.Hour, time.Nanosecond)

	handle := script.run()
	defer handle.cancel() // cleanup

	// Check for UpdateTTL call
	select {
	case update := <-hb.heartbeats:
		require.Equal(t, update.output, context.DeadlineExceeded.Error())
		require.Equal(t, update.status, api.HealthCritical)
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for script check to timeout")
	}
}

// TestScript_Exec_Shutdown asserts a script will be executed once more
// when told to shutdown.
func TestScript_Exec_Shutdown(t *testing.T) {
	shutdown := make(chan struct{})
	exec := newSimpleExec(0, nil)
	logger := testlog.HCLogger(t)
	hb := newFakeHeartbeater()
	script := newScriptMock(hb, exec, logger, time.Hour, 3*time.Second)
	script.shutdownCh = shutdown

	handle := script.run()
	defer handle.cancel() // cleanup
	close(shutdown)       // tell scriptCheck to exit

	select {
	case update := <-hb.heartbeats:
		require.Equal(t, update.output, "code=0 err=<nil>")
		require.Equal(t, update.status, api.HealthPassing)
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for script check to exit")
	}

	select {
	case <-handle.wait(): // ok!
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for script check to exit")
	}
}

// TestScript_Exec_Codes asserts script exit codes are translated to their
// corresponding Consul health check status.
func TestScript_Exec_Codes(t *testing.T) {

	exec := newScriptedExec([]execResult{
		{[]byte("output"), 1, nil},
		{[]byte("output"), 0, nil},
		{[]byte("output"), 0, context.DeadlineExceeded},
		{[]byte("output"), 0, nil},
		{[]byte("<ignored output>"), 2, fmt.Errorf("some error")},
		{[]byte("output"), 0, nil},
		{[]byte("error9000"), 9000, nil},
	})
	logger := testlog.HCLogger(t)
	hb := newFakeHeartbeater()
	script := newScriptMock(
		hb, exec, logger, time.Nanosecond, 3*time.Second)

	handle := script.run()
	defer handle.cancel() // cleanup
	deadline := time.After(3 * time.Second)

	expected := []heartbeat{
		{script.id, "output", api.HealthWarning},
		{script.id, "output", api.HealthPassing},
		{script.id, context.DeadlineExceeded.Error(), api.HealthCritical},
		{script.id, "output", api.HealthPassing},
		{script.id, "some error", api.HealthCritical},
		{script.id, "output", api.HealthPassing},
		{script.id, "error9000", api.HealthCritical},
	}

	for i := 0; i <= 6; i++ {
		select {
		case update := <-hb.heartbeats:
			require.Equal(t, update, expected[i],
				"expected update %d to be '%s' but received '%s'",
				i, expected[i], update)
		case <-deadline:
			t.Fatalf("timed out waiting for all script checks to finish")
		}
	}
}
