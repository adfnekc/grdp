// Package glog is the logging shim the protocol layers write through.
//
// It exists because the upstream fork logged through a package that pulled in a
// large dependency, and this library only needs levelled output with a settable
// destination. Setting.LogLevel takes a [LEVEL], so the levels are part of the
// public surface even though nothing else here is meant to be used by a caller.
//
// Levels run from TRACE, which is very loud and includes packet hex dumps, to
// NONE. INFO is the default. cmd/rdpcli takes a -log flag in the same numbering,
// where a lower number is more output, which is the opposite of what most people
// expect and is worth knowing before wondering why nothing is being printed.
//
// Nothing here logs credentials. A test in the root package fails the build if a
// log call in this repository is handed a password field.
package glog
