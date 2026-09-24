package forgejo

import "strings"

// AcquisitionConfig bounds the single task-fetch request made by one-job.
// Do not combine this with --wait: that flag retries empty/failed fetches
// forever. Runner v12.10.1 applies fetch_timeout only to the fetch RPC, not
// to the separate context used to execute an acquired task.
// Keep the runner's normal job timeout; never put a short deadline around
// the whole one-job process.
const AcquisitionConfig = "runner:\n  fetch_timeout: 2m\n"

// OneJobArgs requests one task without retrying forever when no task is
// available. The runner exits with status 2 on an empty or failed fetch.
// The token stays in a file written via stdin, never in argv.
func OneJobArgs(url, uuid string, labels []string, handle string) []string {
	return []string{
		"one-job", "--url", url, "--uuid", uuid,
		"--token-url", "file:/tmp/tok",
		"--label", strings.Join(labels, ","), "--handle", handle,
		"--config", "/tmp/runner-cfg.yml",
	}
}
