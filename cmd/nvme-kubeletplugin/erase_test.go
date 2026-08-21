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
	"errors"
	"testing"
	"time"
)

type runCall struct {
	name string
	args []string
}

type fakeCommandRunner struct {
	calls   []runCall
	errFor  map[string]error // keyed by device path (last arg before flags)
	failAll error
}

func (f *fakeCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, runCall{name: name, args: args})
	if f.failAll != nil {
		return nil, f.failAll
	}
	if len(args) > 1 {
		if err, ok := f.errFor[args[1]]; ok {
			return nil, err
		}
	}
	return []byte("ok"), nil
}

func withFakeCommandRunner(t *testing.T, f *fakeCommandRunner) {
	t.Helper()
	old := cmdRunner
	cmdRunner = f
	t.Cleanup(func() { cmdRunner = old })
}

func withShrunkDevicePresenceTiming(t *testing.T) {
	t.Helper()
	oldTimeout, oldPoll := devicePresenceTimeout, devicePresencePollInterval
	devicePresenceTimeout = 200 * time.Millisecond
	devicePresencePollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		devicePresenceTimeout, devicePresencePollInterval = oldTimeout, oldPoll
	})
}

func TestWaitForDevicePath(t *testing.T) {
	withShrunkDevicePresenceTiming(t)

	t.Run("present immediately", func(t *testing.T) {
		m := newMockSysfs()
		m.stats["/dev/nvme0n1"] = true
		withMockSysfs(t, m)

		if err := waitForDevicePath(t.Context(), "/dev/nvme0n1"); err != nil {
			t.Errorf("waitForDevicePath() = %v, want nil", err)
		}
	})

	t.Run("never appears times out", func(t *testing.T) {
		m := newMockSysfs()
		withMockSysfs(t, m)

		err := waitForDevicePath(t.Context(), "/dev/nvme0n1")
		if err == nil {
			t.Fatal("waitForDevicePath() = nil, want timeout error")
		}
	})

	t.Run("ctx canceled returns promptly", func(t *testing.T) {
		m := newMockSysfs()
		withMockSysfs(t, m)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := waitForDevicePath(ctx, "/dev/nvme0n1")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waitForDevicePath() = %v, want context.Canceled", err)
		}
	})
}

func TestSecureErase(t *testing.T) {
	withShrunkDevicePresenceTiming(t)
	oldTimeout := secureEraseTimeout
	secureEraseTimeout = 200 * time.Millisecond
	t.Cleanup(func() { secureEraseTimeout = oldTimeout })

	t.Run("erases all paths", func(t *testing.T) {
		m := newMockSysfs()
		m.stats["/dev/nvme0n1"] = true
		m.stats["/dev/nvme0n2"] = true
		withMockSysfs(t, m)

		runner := &fakeCommandRunner{}
		withFakeCommandRunner(t, runner)

		if err := secureErase(t.Context(), []string{"/dev/nvme0n1", "/dev/nvme0n2"}); err != nil {
			t.Fatalf("secureErase() = %v, want nil", err)
		}
		if len(runner.calls) != 2 {
			t.Fatalf("got %d nvme calls, want 2", len(runner.calls))
		}
		for i, path := range []string{"/dev/nvme0n1", "/dev/nvme0n2"} {
			c := runner.calls[i]
			if c.name != "nvme" || c.args[0] != "format" || c.args[1] != path || c.args[2] != "--ses=2" {
				t.Errorf("call %d = %+v, want format %s --ses=2 ...", i, c, path)
			}
		}
	})

	t.Run("stops at first failure", func(t *testing.T) {
		m := newMockSysfs()
		m.stats["/dev/nvme0n1"] = true
		m.stats["/dev/nvme0n2"] = true
		withMockSysfs(t, m)

		runner := &fakeCommandRunner{errFor: map[string]error{"/dev/nvme0n1": errors.New("invalid field (crypto erase unsupported)")}}
		withFakeCommandRunner(t, runner)

		err := secureErase(t.Context(), []string{"/dev/nvme0n1", "/dev/nvme0n2"})
		if err == nil {
			t.Fatal("secureErase() = nil, want error")
		}
		if len(runner.calls) != 1 {
			t.Errorf("got %d nvme calls, want 1 (should stop at first failure)", len(runner.calls))
		}
	})

	t.Run("device path never appears fails without invoking nvme", func(t *testing.T) {
		m := newMockSysfs()
		withMockSysfs(t, m)

		runner := &fakeCommandRunner{}
		withFakeCommandRunner(t, runner)

		err := secureErase(t.Context(), []string{"/dev/nvme0n1"})
		if err == nil {
			t.Fatal("secureErase() = nil, want error")
		}
		if len(runner.calls) != 0 {
			t.Errorf("got %d nvme calls, want 0", len(runner.calls))
		}
	})
}
