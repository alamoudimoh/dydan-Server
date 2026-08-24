package httpx

import (
	"net"
	"os"
	"syscall"
)

// newBrokenPipeError mimics a broken-pipe write error so the recovery
// middleware's broken-pipe branch is exercised without a real socket.
func newBrokenPipeError() error {
	return &net.OpError{
		Op:  "write",
		Err: os.NewSyscallError("write", syscall.EPIPE),
	}
}
