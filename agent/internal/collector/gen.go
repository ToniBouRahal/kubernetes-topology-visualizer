// Package collector loads the BPF program, drains its ring buffer, and exposes kernel-side
// counters. It owns everything that touches the kernel; nothing above it imports cilium/ebpf.
package collector

// bpf2go compiles bpf/tcp_connect.bpf.c and generates the Go bindings plus an embedded object.
// Run via `make generate`, which executes this inside the pinned builder container — the host
// has clang 14 and no libbpf headers.
//
// -target amd64 emits little-endian bindings (tcpconnect_bpfel.go). IPv6 and other
// architectures are out of scope for this release (ADR-001 §4.2).
//
// bpf2go is pinned as a tool dependency in go.mod, so the generator version is reproducible.
//
// -type event emits a Go mirror of `struct event` (TcpConnectEvent) generated from the object's
// BTF. That generated struct is what makes the C/Go layout agreement checkable rather than
// assumed — test T-2.1 asserts it against our own decoding type.
//
//go:generate go tool bpf2go -cc clang -target amd64 -type event -cflags "-O2 -g -Wall -Werror -I/usr/include/x86_64-linux-gnu" TcpConnect ../../bpf/tcp_connect.bpf.c
