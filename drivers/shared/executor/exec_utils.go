package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins/drivers"
	dproto "github.com/hashicorp/nomad/plugins/drivers/proto"
	"github.com/kr/pty"
	"golang.org/x/sys/unix"
)

func cmdExitResult(ps *os.ProcessState, err error) *drivers.ExecTaskStreamingResponseMsg {
	exitCode := -1

	if ps == nil {
		if ee, ok := err.(*exec.ExitError); ok {
			ps = ee.ProcessState
		}
	}

	if ps == nil {
		exitCode = -2
	} else if status, ok := ps.Sys().(syscall.WaitStatus); ok {
		exitCode = status.ExitStatus()
		if status.Signaled() {
			const exitSignalBase = 128
			signal := int(status.Signal())
			exitCode = exitSignalBase + signal
		}
	}

	return &drivers.ExecTaskStreamingResponseMsg{
		Exited: true,
		Result: &dproto.ExitResult{
			ExitCode: int32(exitCode),
		},
	}
}

func handleStdin(logger hclog.Logger, stdin io.WriteCloser, stream drivers.ExecTaskStream, errCh chan<- error) {
	go func() {
		for {
			m, err := stream.Recv()
			logger.Info("received stdin", "msg", m, "error", err)
			if isClosedError(err) {
				return
			} else if err != nil {
				errCh <- err
				return
			}

			if m.Stdin != nil && len(m.Stdin.Data) != 0 {
				_, err := stdin.Write(m.Stdin.Data)
				if err != nil {
					errCh <- err
					return
				}
			} else if m.Stdin != nil && m.Stdin.Close {
				stdin.Close()
			} else if m.TtySize != nil {
				if f, ok := stdin.(*os.File); ok {
					pty.Setsize(f, &pty.Winsize{
						Rows: uint16(m.TtySize.Height),
						Cols: uint16(m.TtySize.Width),
					})
				} else {
					errCh <- fmt.Errorf("attempted to resize a non-tty session")
					return
				}

			} else {
				// ignore heartbeats or unexpected tty events
			}
		}
	}()
}

func handleStdout(logger hclog.Logger, reader io.Reader, wg *sync.WaitGroup, send func(*drivers.ExecTaskStreamingResponseMsg) error, errCh chan<- error) {
	wg.Add(1)

	go func() {
		defer wg.Done()

		for {
			buf := make([]byte, 4096)
			n, err := reader.Read(buf)
			logger.Info("sending stdout", "bytes", string(buf[:n]), "error", err, "isioe", isClosedError(err))
			if isClosedError(err) {
				if err := send(&drivers.ExecTaskStreamingResponseMsg{
					Stdout: &dproto.ExecTaskStreamingOperation{
						Close: true,
					},
				}); err != nil {
					errCh <- err
					return
				}
				return
			} else if err != nil {
				errCh <- err
				return
			}

			// tty only reportsstdout
			if err := send(&drivers.ExecTaskStreamingResponseMsg{
				Stdout: &dproto.ExecTaskStreamingOperation{
					Data: buf[:n],
				},
			}); err != nil {
				errCh <- err
				return
			}

		}
	}()
}

func handleStderr(logger hclog.Logger, reader io.Reader, wg *sync.WaitGroup, send func(*drivers.ExecTaskStreamingResponseMsg) error, errCh chan<- error) {
	wg.Add(1)

	go func() {
		defer wg.Done()

		for {
			buf := make([]byte, 4096)
			n, err := reader.Read(buf)
			logger.Info("sending stdout", "bytes", string(buf[:n]), "error", err)
			if isClosedError(err) {
				if err := send(&drivers.ExecTaskStreamingResponseMsg{
					Stderr: &dproto.ExecTaskStreamingOperation{
						Close: true,
					},
				}); err != nil {
					errCh <- err
					return
				}
				return
			} else if err != nil {
				errCh <- err
				return
			}

			if err := send(&drivers.ExecTaskStreamingResponseMsg{
				Stderr: &dproto.ExecTaskStreamingOperation{
					Data: buf[:n],
				},
			}); err != nil {
				errCh <- err
				return
			}

		}
	}()
}

func isClosedError(err error) bool {
	if err == nil {
		return false
	}

	return err == io.EOF ||
		err == io.ErrClosedPipe ||
		strings.Contains(err.Error(), unix.EIO.Error())
}

type execHelper struct {
	logger hclog.Logger

	newTerminal func() (master func() (*os.File, error), slave *os.File, err error)
	setTTY      func(*os.File) error
	setIO       func(stdin io.Reader, stdout, stderr io.Writer) error

	processStart func() error
	processWait  func() (*os.ProcessState, error)
}

func (e *execHelper) run(ctx context.Context, tty bool, stream drivers.ExecTaskStream) error {
	if tty {
		return e.runTTY(ctx, stream)
	}
	return e.runNoTTY(ctx, stream)
}

func (e *execHelper) runTTY(ctx context.Context, stream drivers.ExecTaskStream) error {
	ptyF, tty, err := e.newTerminal()
	if err != nil {
		return fmt.Errorf("failed to open a tty: %v", err)
	}
	defer tty.Close()

	if err := e.setTTY(tty); err != nil {
		return fmt.Errorf("failed to set command tty: %v", err)
	}
	if err := e.processStart(); err != nil {
		return fmt.Errorf("failed to start command: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	pty, err := ptyF()
	if err != nil {
		return fmt.Errorf("failed to get tty master: %v", err)
	}

	defer pty.Close()
	handleStdin(e.logger, pty, stream, errCh)
	// when tty is on, stdout and stderr point to the same pty so only read once
	handleStdout(e.logger, pty, &wg, stream.Send, errCh)

	ps, err := e.processWait()
	e.logger.Warn("command done", "error", err)
	if err != nil {
		e.logger.Warn("failed to wait for cmd", "error", err)
	}

	tty.Close()

	// wait until we get all process output
	wg.Wait()

	// wait to flush out output
	stream.Send(cmdExitResult(ps, err))

	select {
	case cerr := <-errCh:
		return cerr
	default:
		return nil
	}
}

func (e *execHelper) runNoTTY(ctx context.Context, stream drivers.ExecTaskStream) error {
	var sendLock sync.Mutex
	send := func(v *drivers.ExecTaskStreamingResponseMsg) error {
		sendLock.Lock()
		defer sendLock.Unlock()

		return stream.Send(v)
	}

	stdinPr, stdinPw := io.Pipe()
	stdoutPr, stdoutPw := io.Pipe()
	stderrPr, stderrPw := io.Pipe()

	defer stdoutPw.Close()
	defer stderrPw.Close()

	if err := e.setIO(stdinPr, stdoutPw, stderrPw); err != nil {
		return fmt.Errorf("failed to set command io: %v", err)
	}

	if err := e.processStart(); err != nil {
		return fmt.Errorf("failed to start command: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	handleStdin(e.logger, stdinPw, stream, errCh)
	handleStdout(e.logger, stdoutPr, &wg, send, errCh)
	handleStderr(e.logger, stderrPr, &wg, send, errCh)

	ps, err := e.processWait()
	e.logger.Warn("command done", "error", err)
	if err != nil {
		e.logger.Warn("failed to wait for cmd", "error", err)
	}

	stdoutPw.Close()
	stderrPw.Close()

	// wait until we get all process output
	wg.Wait()

	// wait to flush out output
	stream.Send(cmdExitResult(ps, err))

	select {
	case cerr := <-errCh:
		return cerr
	default:
		return nil
	}
}