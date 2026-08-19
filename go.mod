module github.com/byteford/nomad-driver-steamcmd

go 1.22

require (
	github.com/hashicorp/go-hclog v1.6.3
	github.com/hashicorp/nomad v2.0.5+incompatible
	github.com/shirou/gopsutil/v3 v3.24.5
	github.com/stretchr/testify v1.9.0
)

// armon/go-metrics renamed its module path to hashicorp/go-metrics at
// v0.5.0 (API-compatible, path-only rename). Nomad's dependency graph
// (via hashicorp/raft) still references the old github.com/armon/go-metrics
// import path, and Go's module resolution can select a post-rename
// version under that old path, which then fails because that version's
// own go.mod declares itself as github.com/hashicorp/go-metrics instead.
// This is a known, widely-hit issue across the Hashicorp Go ecosystem;
// see https://github.com/hashicorp/serf/issues/707. The fix is to
// redirect the old path to the real module at the same version -- this
// is a straight path swap, not a version change, so it doesn't affect
// behavior.
replace github.com/armon/go-metrics => github.com/hashicorp/go-metrics v0.5.3
