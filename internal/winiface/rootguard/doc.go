// Package rootguard keeps a root test from leaving the machine changed when the
// test process is killed. It is for the tests that run from an elevated shell
// and write system state that outlives the process: a DNS rule, a route, an
// address, a TAP adapter, a service.
//
// t.Cleanup, defer and the other ways a test undoes its work do not run when
// the process ends the hard way (Ctrl+C, go test -timeout, a crash, the window
// closed). A catch-all DNS rule that points to a throw-away resolver on
// 127.0.0.1:53 would then keep all name resolution of the PC dead, across
// reboots. A guard is a separate process that is started before the change
// and removes what the test would have removed:
//
//   - when its parent dies without having released it (the pipe it reads from
//     closes), and
//   - when its deadline passes, if the test hangs.
//
// The test releases it when it is done and has cleaned up itself; the guard
// then ends without doing anything.
//
// The guard is the test binary started again in a helper mode, so that it has
// the recovery code of the test: each test package registers its recoveries
// with Register in an init function, defines the one test function that runs
// the helper (see HelperTestName and RunHelper), and calls Arm before it
// changes anything:
//
//	func init() { rootguard.Register("dns-sweep", func(string) error { ... }) }
//
//	func TestRootGuardHelper(t *testing.T) { rootguard.RunHelper(t) }
//
//	rootguard.Arm(t, rootguard.Plan{Action: "dns-sweep", Within: 2 * time.Minute, Manual: "..."})
//
// Arm prints the manual recovery command as well, so that a person who finds
// the machine in the state of a killed test, with the guard killed as well, has
// the exact command.
//
// The guard starts without a console and in a process group of its own, so
// that Ctrl+C, which the console sends to every process attached to it, does
// not end it with the test. It does not survive a job object that kills its
// processes when it closes, nor the end of the Windows session; the manual
// command is for that.
package rootguard
