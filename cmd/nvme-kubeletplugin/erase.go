// Copyright 2026 Red Hat, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// secureEraseTimeout bounds each nvme-format call. Cryptographic erase
// (--ses=2) is near-instant by NVMe spec design regardless of capacity, so
// this is generous for the happy path and tight enough that a genuine hang
// still fails closed promptly. Var (not const) so tests can shrink it.
var secureEraseTimeout = 30 * time.Second

// devicePresenceTimeout/devicePresencePollInterval bound how long we wait for
// a namespace block device path to reappear after a VFIO-to-nvme driver
// rebind, since kernel namespace scanning after bind can be asynchronous.
// Vars (not consts) so tests can shrink them.
var (
	devicePresenceTimeout      = 10 * time.Second
	devicePresencePollInterval = 100 * time.Millisecond
)

// CommandRunner abstracts external command execution so erase logic is
// unit-testable, mirroring the SysfsOps seam in sysfs.go.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type realCommandRunner struct{}

func (realCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %v: %w (output: %s)", name, args, err, out)
	}
	return out, nil
}

var cmdRunner CommandRunner = realCommandRunner{}

// waitForDevicePath polls for path to exist, bounded by devicePresenceTimeout.
func waitForDevicePath(ctx context.Context, path string) error {
	deadline := time.Now().Add(devicePresenceTimeout)
	for {
		if _, err := sysfs.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("device path %s did not appear within %s", path, devicePresenceTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(devicePresencePollInterval):
		}
	}
}

// secureErase cryptographically erases each device path in turn via
// `nvme format --ses=2` (Cryptographic Erase). Crypto erase discards and
// regenerates the drive's internal encryption key rather than overwriting
// media, so it is near-instant regardless of capacity — this keeps the erase
// window compatible with the kubeletplugin helper's default serialization of
// Prepare/Unprepare calls (see driver.go / NewDriver). Drives that don't
// support crypto erase fail the format command immediately with an
// invalid-field error, which surfaces here as a wrapped error (fail-closed);
// there is no fallback to a slower User Data Erase.
//
// ctx must be a long-lived, driver-lifetime context (e.g. DeviceState.baseCtx)
// — NOT the short-lived context of the incoming Unprepare gRPC call. The
// kubelet may cancel that RPC's context on its own timeout, and killing an
// in-progress format command mid-flight would leave the device in a worse,
// indeterminate state than not starting the erase at all. Callers still block
// on this function's return before responding to the RPC. Note this only
// decouples erase from the RPC's cancellation, not from all cancellation:
// baseCtx is itself canceled on driver shutdown (SIGTERM), so a node drain
// mid-erase can still interrupt an in-progress format command — an accepted,
// unavoidable tradeoff since the process is exiting regardless.
func secureErase(ctx context.Context, devicePaths []string) error {
	for _, path := range devicePaths {
		if err := waitForDevicePath(ctx, path); err != nil {
			return fmt.Errorf("secure erase of %s: %w", path, err)
		}

		eraseCtx, cancel := context.WithTimeout(ctx, secureEraseTimeout)
		_, err := cmdRunner.Run(eraseCtx, "nvme", "format", path, "--ses=2", "--force")
		cancel()
		if err != nil {
			return fmt.Errorf("secure erase of %s failed: %w", path, err)
		}
	}
	return nil
}
